#pragma once

#include <cstddef>
#include <memory>
#include <stdexcept>
#include <string>
#include <vector>

namespace ambivox {

struct Audio {
    std::vector<float> samples;
    int sample_rate = 0;
};

struct Unsupported : std::runtime_error {
    using std::runtime_error::runtime_error;
};

struct ModelError : std::runtime_error {
    using std::runtime_error::runtime_error;
};

class Backend {
public:
    virtual ~Backend() = default;
    virtual const char * name() const = 0;
    virtual std::string transcribe(const float * pcm, size_t n, const std::string & lang) = 0;
    virtual std::string translate(const std::string & text, const std::string & source,
                                  const std::string & target) = 0;
    virtual Audio synthesize(const std::string & text, const std::string & lang) = 0;
};

std::unique_ptr<Backend> make_sim_backend();

#ifdef AMBIVOX_WITH_SUSSURRO
std::unique_ptr<Backend> make_sussurro_backend(const std::string & model_dir, int threads,
                                               const std::string & asr_model);
#endif

}
