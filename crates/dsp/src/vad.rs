use std::collections::VecDeque;

use crate::kernels;

#[derive(Clone, Debug)]
pub struct SegmenterConfig {
    pub sample_rate: u32,
    pub frame_ms: u32,
    pub threshold_db: f32,
    pub min_level_db: f32,
    pub attack_ms: u32,
    pub hangover_ms: u32,
    pub pre_roll_ms: u32,
    pub min_utterance_ms: u32,
    pub max_utterance_ms: u32,
}

impl Default for SegmenterConfig {
    fn default() -> Self {
        Self {
            sample_rate: 16000,
            frame_ms: 20,
            threshold_db: 9.0,
            min_level_db: -50.0,
            attack_ms: 40,
            hangover_ms: 600,
            pre_roll_ms: 300,
            min_utterance_ms: 200,
            max_utterance_ms: 15_000,
        }
    }
}

const FLOOR_MIN_DB: f32 = -75.0;
const FLOOR_MAX_DB: f32 = -30.0;
const FLOOR_FALL: f32 = 0.2;
const FLOOR_RISE: f32 = 0.02;
const FLOOR_CREEP_DB: f32 = 0.02;
const TRAILING_MS: u32 = 200;

pub struct Segmenter {
    config: SegmenterConfig,
    frame_len: usize,
    pending: Vec<f32>,
    pre_roll: VecDeque<f32>,
    utterance: Vec<f32>,
    floor_db: f32,
    speech_frames: u32,
    silence_frames: u32,
    voiced_frames: u32,
    active: bool,
}

impl Segmenter {
    pub fn new(config: SegmenterConfig) -> Self {
        let frame_len = (config.sample_rate * config.frame_ms / 1000) as usize;
        assert!(frame_len > 0);
        Self {
            config,
            frame_len,
            pending: Vec::new(),
            pre_roll: VecDeque::new(),
            utterance: Vec::new(),
            floor_db: -60.0,
            speech_frames: 0,
            silence_frames: 0,
            voiced_frames: 0,
            active: false,
        }
    }

    pub fn is_active(&self) -> bool {
        self.active
    }

    pub fn noise_floor_db(&self) -> f32 {
        self.floor_db
    }

    pub fn push(&mut self, samples: &[f32], mut emit: impl FnMut(Vec<f32>)) {
        self.pending.extend_from_slice(samples);
        let mut offset = 0;
        while offset + self.frame_len <= self.pending.len() {
            let frame: Vec<f32> = self.pending[offset..offset + self.frame_len].to_vec();
            offset += self.frame_len;
            self.push_frame(&frame, &mut emit);
        }
        self.pending.drain(..offset);
    }

    pub fn flush(&mut self, mut emit: impl FnMut(Vec<f32>)) {
        self.pending.clear();
        if self.active {
            self.finish(&mut emit);
        }
        self.pre_roll.clear();
        self.speech_frames = 0;
    }

    fn frames(&self, ms: u32) -> u32 {
        ms.div_ceil(self.config.frame_ms).max(1)
    }

    fn samples(&self, ms: u32) -> usize {
        (self.config.sample_rate as u64 * ms as u64 / 1000) as usize
    }

    fn push_frame(&mut self, frame: &[f32], emit: &mut impl FnMut(Vec<f32>)) {
        let level_db = 10.0 * (kernels::energy(frame) / frame.len() as f32 + 1e-12).log10();
        let speech = level_db > self.floor_db + self.config.threshold_db && level_db > self.config.min_level_db;
        self.track_floor(level_db, speech);

        if !self.active {
            self.pre_roll.extend(frame);
            let keep = self.samples(self.config.pre_roll_ms);
            while self.pre_roll.len() > keep {
                self.pre_roll.pop_front();
            }
            self.speech_frames = if speech { self.speech_frames + 1 } else { 0 };
            if self.speech_frames >= self.frames(self.config.attack_ms) {
                self.active = true;
                self.voiced_frames = self.speech_frames;
                self.silence_frames = 0;
                self.utterance.extend(self.pre_roll.drain(..));
            }
            return;
        }

        self.utterance.extend_from_slice(frame);
        if speech {
            self.silence_frames = 0;
            self.voiced_frames += 1;
        } else {
            self.silence_frames += 1;
        }

        let too_long = self.utterance.len() >= self.samples(self.config.max_utterance_ms);
        if too_long || self.silence_frames >= self.frames(self.config.hangover_ms) {
            self.finish(emit);
        }
    }

    fn track_floor(&mut self, level_db: f32, speech: bool) {
        if speech {
            self.floor_db += FLOOR_CREEP_DB;
        } else if level_db < self.floor_db {
            self.floor_db += FLOOR_FALL * (level_db - self.floor_db);
        } else {
            self.floor_db += FLOOR_RISE * (level_db - self.floor_db);
        }
        self.floor_db = self.floor_db.clamp(FLOOR_MIN_DB, FLOOR_MAX_DB);
    }

    fn finish(&mut self, emit: &mut impl FnMut(Vec<f32>)) {
        let silence = self.silence_frames as usize * self.frame_len;
        let trim = silence.saturating_sub(self.samples(TRAILING_MS));
        let mut utterance = std::mem::take(&mut self.utterance);
        utterance.truncate(utterance.len() - trim.min(utterance.len()));

        let voiced_ms = self.voiced_frames * self.config.frame_ms;
        self.active = false;
        self.speech_frames = 0;
        self.silence_frames = 0;
        self.voiced_frames = 0;
        if voiced_ms >= self.config.min_utterance_ms {
            emit(utterance);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const RATE: u32 = 16000;

    fn tone(seconds: f64, amplitude: f32) -> Vec<f32> {
        (0..(RATE as f64 * seconds) as usize)
            .map(|i| amplitude * (2.0 * std::f32::consts::PI * 440.0 * i as f32 / RATE as f32).sin())
            .collect()
    }

    fn noise(seconds: f64, amplitude: f32, seed: u64) -> Vec<f32> {
        let mut state = seed;
        (0..(RATE as f64 * seconds) as usize)
            .map(|_| {
                state = state.wrapping_mul(6364136223846793005).wrapping_add(1442695040888963407);
                amplitude * (((state >> 40) as f32 / (1u64 << 24) as f32) * 2.0 - 1.0)
            })
            .collect()
    }

    fn run(config: SegmenterConfig, audio: &[f32]) -> Vec<Vec<f32>> {
        let mut segmenter = Segmenter::new(config);
        let mut out = Vec::new();
        for chunk in audio.chunks(313) {
            segmenter.push(chunk, |u| out.push(u));
        }
        segmenter.flush(|u| out.push(u));
        out
    }

    fn seconds(samples: &[f32]) -> f64 {
        samples.len() as f64 / RATE as f64
    }

    #[test]
    fn silence_produces_nothing() {
        assert!(run(SegmenterConfig::default(), &vec![0.0; RATE as usize * 3]).is_empty());
        assert!(run(SegmenterConfig::default(), &noise(3.0, 0.0005, 1)).is_empty());
    }

    #[test]
    fn one_burst_becomes_one_utterance() {
        let mut audio = noise(1.0, 0.0005, 2);
        audio.extend(tone(1.0, 0.3));
        audio.extend(noise(1.5, 0.0005, 3));
        let utterances = run(SegmenterConfig::default(), &audio);
        assert_eq!(utterances.len(), 1);
        let length = seconds(&utterances[0]);
        assert!((1.2..=1.6).contains(&length), "utterance lasted {length} s");
    }

    #[test]
    fn pre_roll_keeps_the_onset() {
        let mut audio = vec![0.0; RATE as usize];
        audio.extend(tone(0.5, 0.3));
        audio.extend(vec![0.0; RATE as usize]);
        let utterances = run(SegmenterConfig::default(), &audio);
        assert_eq!(utterances.len(), 1);
        let first_loud = utterances[0].iter().position(|s| s.abs() > 0.1).unwrap();
        assert!(first_loud >= RATE as usize / 5, "onset at sample {first_loud}");
    }

    #[test]
    fn short_pauses_do_not_split() {
        let mut audio = vec![0.0; 8000];
        audio.extend(tone(0.5, 0.3));
        audio.extend(vec![0.0; 4800]);
        audio.extend(tone(0.5, 0.3));
        audio.extend(vec![0.0; 16000]);
        assert_eq!(run(SegmenterConfig::default(), &audio).len(), 1);
    }

    #[test]
    fn long_pauses_split() {
        let mut audio = vec![0.0; 8000];
        audio.extend(tone(0.5, 0.3));
        audio.extend(vec![0.0; 16000]);
        audio.extend(tone(0.5, 0.3));
        audio.extend(vec![0.0; 16000]);
        assert_eq!(run(SegmenterConfig::default(), &audio).len(), 2);
    }

    #[test]
    fn clicks_are_ignored() {
        let mut audio = vec![0.0; 16000];
        audio.extend(tone(0.06, 0.5));
        audio.extend(vec![0.0; 32000]);
        assert!(run(SegmenterConfig::default(), &audio).is_empty());
    }

    #[test]
    fn long_speech_is_cut_at_the_limit() {
        let config = SegmenterConfig { max_utterance_ms: 2000, ..SegmenterConfig::default() };
        let mut audio = vec![0.0; 8000];
        audio.extend(tone(5.0, 0.3));
        audio.extend(vec![0.0; 16000]);
        let utterances = run(config, &audio);
        assert!(utterances.len() >= 2, "got {} utterances", utterances.len());
        assert!(utterances.iter().all(|u| seconds(u) <= 2.05));
    }

    #[test]
    fn flush_emits_the_open_utterance() {
        let mut audio = vec![0.0; 8000];
        audio.extend(tone(1.0, 0.3));
        let utterances = run(SegmenterConfig::default(), &audio);
        assert_eq!(utterances.len(), 1);
    }

    #[test]
    fn speech_over_steady_noise_is_detected() {
        let mut audio = noise(2.0, 0.01, 4);
        let mut burst = noise(1.0, 0.01, 5);
        for (b, t) in burst.iter_mut().zip(tone(1.0, 0.3)) {
            *b += t;
        }
        audio.extend(burst);
        audio.extend(noise(2.0, 0.01, 6));
        assert_eq!(run(SegmenterConfig::default(), &audio).len(), 1);
    }
}
