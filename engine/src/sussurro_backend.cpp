#include "backend.h"

#include <cstring>
#include <filesystem>
#include <map>
#include <mutex>
#include <utility>

#include "sherpa-onnx/c-api/c-api.h"
#include "sussurro.h"
#include "whisper.h"

namespace ambivox {

namespace {

namespace fs = std::filesystem;

constexpr int kMaxNewTokens = 256;
constexpr int kBeamSize = 1;
constexpr const char * kWhisperModel = "ggml-small.bin";

struct Route {
    const char * source;
    const char * target;
    const char * model;
    const char * token;
};

constexpr Route kRoutes[] = {
    {"en", "fr", "tc-en-fr.gguf", ""},      {"en", "es", "tc-en-es.gguf", ""},
    {"en", "it", "tc-en-it.gguf", ""},      {"fr", "en", "tc-fr-en.gguf", ""},
    {"it", "en", "tc-it-en.gguf", ""},      {"es", "en", "es-en.gguf", ""},
    {"it", "fr", "tc-itc-itc.gguf", "fra"}, {"es", "fr", "tc-itc-itc.gguf", "fra"},
    {"fr", "it", "tc-itc-itc.gguf", "ita"}, {"es", "it", "tc-itc-itc.gguf", "ita"},
    {"fr", "es", "tc-itc-itc.gguf", "spa"}, {"it", "es", "tc-itc-itc.gguf", "spa"},
};

constexpr std::pair<const char *, const char *> kVoices[] = {
    {"en", "vits-piper-en_US-ryan-medium"},
    {"fr", "vits-piper-fr_FR-tom-medium"},
    {"es", "vits-piper-es_ES-davefx-medium"},
    {"it", "vits-piper-it_IT-paola-medium"},
};

struct Translator {
    sussurro_model model;
    sussurro_tokenizer * tokenizer = nullptr;
    std::mutex mutex;

    ~Translator() {
        if (tokenizer != nullptr) sussurro_tokenizer_free(tokenizer);
        sussurro_model_free(model);
    }
};

struct Voice {
    const SherpaOnnxOfflineTts * tts = nullptr;
    std::mutex mutex;

    ~Voice() {
        if (tts != nullptr) SherpaOnnxDestroyOfflineTts(tts);
    }
};

std::string find_onnx(const fs::path & dir) {
    std::error_code ec;
    for (const auto & entry : fs::directory_iterator(dir, ec)) {
        if (entry.path().extension() == ".onnx") return entry.path().string();
    }
    return "";
}

std::string trim(const std::string & s) {
    const size_t first = s.find_first_not_of(" \t\r\n");
    if (first == std::string::npos) return "";
    const size_t last = s.find_last_not_of(" \t\r\n");
    return s.substr(first, last - first + 1);
}

class SussurroBackend final : public Backend {
public:
    SussurroBackend(const std::string & model_dir, int threads)
        : dir_(model_dir), threads_(threads > 0 ? threads : 4) {
        if (!fs::is_directory(dir_)) throw ModelError("model directory not found: " + model_dir);
    }

    ~SussurroBackend() override {
        translators_.clear();
        voices_.clear();
        if (whisper_ != nullptr) whisper_free(whisper_);
    }

    const char * name() const override { return "sussurro"; }

    std::string transcribe(const float * pcm, size_t n, const std::string & lang) override {
        whisper_full_params params = whisper_full_default_params(WHISPER_SAMPLING_GREEDY);
        params.language = lang.c_str();
        params.translate = false;
        params.no_timestamps = true;
        params.no_context = true;
        params.single_segment = false;
        params.suppress_blank = true;
        params.print_progress = false;
        params.print_realtime = false;
        params.print_special = false;
        params.print_timestamps = false;
        params.n_threads = threads_;

        std::lock_guard<std::mutex> lock(whisper_mutex_);
        if (whisper_ == nullptr) {
            const std::string path = (dir_ / kWhisperModel).string();
            if (!fs::exists(path)) throw ModelError("whisper model not found: " + path);
            whisper_ = whisper_init_from_file_with_params(path.c_str(), whisper_context_default_params());
            if (whisper_ == nullptr) throw ModelError("cannot load whisper model: " + path);
        }
        if (whisper_full(whisper_, params, pcm, (int) n) != 0) throw std::runtime_error("whisper inference failed");
        std::string text;
        const int segments = whisper_full_n_segments(whisper_);
        for (int i = 0; i < segments; ++i) text += whisper_full_get_segment_text(whisper_, i);
        return trim(text);
    }

    std::string translate(const std::string & text, const std::string & source,
                          const std::string & target) override {
        if (source == target || text.empty()) return text;
        const Route * route = nullptr;
        for (const Route & r : kRoutes) {
            if (source == r.source && target == r.target) route = &r;
        }
        if (route == nullptr) throw Unsupported("no translation model for " + source + " -> " + target);

        Translator & translator = this->translator(route->model);
        const std::string token = route->token[0] != '\0' ? ">>" + std::string(route->token) + "<<" : "";
        std::lock_guard<std::mutex> lock(translator.mutex);
        return trim(sussurro_translate(translator.model, translator.tokenizer, text, kMaxNewTokens, kBeamSize, token));
    }

    Audio synthesize(const std::string & text, const std::string & lang) override {
        Voice & voice = this->voice(lang);
        SherpaOnnxGenerationConfig config;
        std::memset(&config, 0, sizeof(config));
        config.speed = 1.0f;
        config.sid = 0;
        config.silence_scale = 0.2f;

        std::lock_guard<std::mutex> lock(voice.mutex);
        const SherpaOnnxGeneratedAudio * generated =
            SherpaOnnxOfflineTtsGenerateWithConfig(voice.tts, text.c_str(), &config, nullptr, nullptr);
        if (generated == nullptr) throw std::runtime_error("speech synthesis failed");
        Audio audio;
        audio.samples.assign(generated->samples, generated->samples + generated->n);
        audio.sample_rate = generated->sample_rate;
        SherpaOnnxDestroyOfflineTtsGeneratedAudio(generated);
        return audio;
    }

private:
    Translator & translator(const std::string & file) {
        std::lock_guard<std::mutex> lock(registry_mutex_);
        auto it = translators_.find(file);
        if (it != translators_.end()) return *it->second;

        auto loaded = std::make_unique<Translator>();
        const std::string path = (dir_ / file).string();
        if (!fs::exists(path)) throw ModelError("translation model not found: " + path);
        if (!sussurro_model_load(path, loaded->model)) throw ModelError("cannot load translation model: " + path);
        loaded->tokenizer = sussurro_tokenizer_create(loaded->model);
        if (loaded->tokenizer == nullptr) throw ModelError("cannot create tokenizer for: " + path);
        return *translators_.emplace(file, std::move(loaded)).first->second;
    }

    Voice & voice(const std::string & lang) {
        std::lock_guard<std::mutex> lock(registry_mutex_);
        auto it = voices_.find(lang);
        if (it != voices_.end()) return *it->second;

        const char * folder = nullptr;
        for (const auto & [code, name] : kVoices) {
            if (lang == code) folder = name;
        }
        if (folder == nullptr) throw Unsupported("no voice for language: " + lang);

        const fs::path voice_dir = dir_ / folder;
        const std::string model = find_onnx(voice_dir);
        if (model.empty()) throw ModelError("no .onnx voice in " + voice_dir.string());
        const std::string tokens = (voice_dir / "tokens.txt").string();
        const std::string data = (voice_dir / "espeak-ng-data").string();

        SherpaOnnxOfflineTtsConfig config;
        std::memset(&config, 0, sizeof(config));
        config.model.vits.model = model.c_str();
        config.model.vits.tokens = tokens.c_str();
        config.model.vits.data_dir = data.c_str();
        config.model.vits.noise_scale = 0.667f;
        config.model.vits.noise_scale_w = 0.8f;
        config.model.vits.length_scale = 1.0f;
        config.model.num_threads = threads_;
        config.model.provider = "cpu";
        config.max_num_sentences = 1;
        config.silence_scale = 0.2f;

        auto loaded = std::make_unique<Voice>();
        loaded->tts = SherpaOnnxCreateOfflineTts(&config);
        if (loaded->tts == nullptr) throw ModelError("cannot load voice: " + model);
        return *voices_.emplace(lang, std::move(loaded)).first->second;
    }

    fs::path dir_;
    int threads_;
    whisper_context * whisper_ = nullptr;
    std::mutex whisper_mutex_;
    std::mutex registry_mutex_;
    std::map<std::string, std::unique_ptr<Translator>> translators_;
    std::map<std::string, std::unique_ptr<Voice>> voices_;
};

}

std::unique_ptr<Backend> make_sussurro_backend(const std::string & model_dir, int threads) {
    return std::make_unique<SussurroBackend>(model_dir, threads);
}

}
