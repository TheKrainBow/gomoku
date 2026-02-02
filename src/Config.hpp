#ifndef CONFIG_HPP
#define CONFIG_HPP

#include <cstddef>

namespace Config {
constexpr bool kGhostMode = true;
constexpr bool kLogDepthScores = false;
constexpr int kAiDepth = 5;
constexpr int kAiTimeoutMs = 0;
constexpr int kAiTopCandidates = 6;
constexpr bool kAiQuickWinExit = true;
constexpr int kAiMoveDelayMs = 0;
constexpr std::size_t kAiTtMaxEntries = 200000;
}

#endif
