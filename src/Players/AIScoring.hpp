#ifndef AISCORING_HPP
#define AISCORING_HPP

#include <cstdint>
#include <unordered_map>
#include <functional>
#include <vector>

#include "GameState.hpp"
#include "Rules.hpp"

struct AISearchCache {
	struct StateKey {
		std::uint64_t hash;
		int boardSize;
		int capturedBlack;
		int capturedWhite;
		GameState::Status status;
		GameState::PlayerColor currentPlayer;
		bool operator==(const StateKey& other) const {
			return hash == other.hash && boardSize == other.boardSize
				&& capturedBlack == other.capturedBlack && capturedWhite == other.capturedWhite
				&& status == other.status && currentPlayer == other.currentPlayer;
		}
	};

	struct StateKeyHash {
		std::size_t operator()(const StateKey& key) const {
			std::size_t h = static_cast<std::size_t>(key.hash);
			h ^= static_cast<std::size_t>(key.boardSize + 17) << 1;
			h ^= static_cast<std::size_t>(key.capturedBlack + 3) << 2;
			h ^= static_cast<std::size_t>(key.capturedWhite + 7) << 3;
			h ^= static_cast<std::size_t>(key.status) << 4;
			h ^= static_cast<std::size_t>(key.currentPlayer == GameState::PlayerColor::Black ? 1 : 2) << 5;
			return h;
		}
	};

	struct TTKey {
		std::uint64_t hash;
		int depthLeft;
		int boardSize;
		int capturedBlack;
		int capturedWhite;
		GameState::Status status;
		GameState::PlayerColor currentPlayer;
		bool operator==(const TTKey& other) const {
			return hash == other.hash && depthLeft == other.depthLeft && boardSize == other.boardSize
				&& capturedBlack == other.capturedBlack && capturedWhite == other.capturedWhite
				&& status == other.status && currentPlayer == other.currentPlayer;
		}
	};

	struct TTKeyHash {
		std::size_t operator()(const TTKey& key) const {
			std::size_t h = static_cast<std::size_t>(key.hash);
			h ^= static_cast<std::size_t>(key.depthLeft + 31) * 1315423911u;
			h ^= static_cast<std::size_t>(key.boardSize + 17) << 1;
			h ^= static_cast<std::size_t>(key.capturedBlack + 3) << 2;
			h ^= static_cast<std::size_t>(key.capturedWhite + 7) << 3;
			h ^= static_cast<std::size_t>(key.status) << 4;
			h ^= static_cast<std::size_t>(key.currentPlayer == GameState::PlayerColor::Black ? 1 : 2) << 5;
			return h;
		}
	};

	struct TTEntry {
		double value;
		int depthLeft;
		Move bestMove;
	};

	struct MoveCacheKey {
		std::uint64_t hash;
		int depthLeft;
		int boardSize;
		int capturedBlack;
		int capturedWhite;
		GameState::Status status;
		GameState::PlayerColor currentPlayer;
		int x;
		int y;
		bool operator==(const MoveCacheKey& other) const {
			return hash == other.hash && depthLeft == other.depthLeft && boardSize == other.boardSize
				&& capturedBlack == other.capturedBlack && capturedWhite == other.capturedWhite
				&& status == other.status && currentPlayer == other.currentPlayer
				&& x == other.x && y == other.y;
		}
	};

	struct MoveCacheKeyHash {
		std::size_t operator()(const MoveCacheKey& key) const {
			std::size_t h = static_cast<std::size_t>(key.hash);
			h ^= static_cast<std::size_t>(key.depthLeft + 31) * 1315423911u;
			h ^= static_cast<std::size_t>(key.boardSize + 17) << 1;
			h ^= static_cast<std::size_t>(key.capturedBlack + 3) << 2;
			h ^= static_cast<std::size_t>(key.capturedWhite + 7) << 3;
			h ^= static_cast<std::size_t>(key.status) << 4;
			h ^= static_cast<std::size_t>(key.currentPlayer == GameState::PlayerColor::Black ? 1 : 2) << 5;
			h ^= static_cast<std::size_t>(key.x + 13) << 6;
			h ^= static_cast<std::size_t>(key.y + 19) << 7;
			return h;
		}
	};

	struct ImmediateWinKey {
		std::uint64_t hash;
		int boardSize;
		int capturedBlack;
		int capturedWhite;
		GameState::Status status;
		GameState::PlayerColor player;
		int x;
		int y;
		bool operator==(const ImmediateWinKey& other) const {
			return hash == other.hash && boardSize == other.boardSize
				&& capturedBlack == other.capturedBlack && capturedWhite == other.capturedWhite
				&& status == other.status && player == other.player
				&& x == other.x && y == other.y;
		}
	};

	struct ImmediateWinKeyHash {
		std::size_t operator()(const ImmediateWinKey& key) const {
			std::size_t h = static_cast<std::size_t>(key.hash);
			h ^= static_cast<std::size_t>(key.boardSize + 17) << 1;
			h ^= static_cast<std::size_t>(key.capturedBlack + 3) << 2;
			h ^= static_cast<std::size_t>(key.capturedWhite + 7) << 3;
			h ^= static_cast<std::size_t>(key.status) << 4;
			h ^= static_cast<std::size_t>(key.player == GameState::PlayerColor::Black ? 1 : 2) << 5;
			h ^= static_cast<std::size_t>(key.x + 13) << 6;
			h ^= static_cast<std::size_t>(key.y + 19) << 7;
			return h;
		}
	};

	struct ImmediateWinStateKey {
		std::uint64_t hash;
		int boardSize;
		int capturedBlack;
		int capturedWhite;
		GameState::Status status;
		GameState::PlayerColor player;
		bool operator==(const ImmediateWinStateKey& other) const {
			return hash == other.hash && boardSize == other.boardSize
				&& capturedBlack == other.capturedBlack && capturedWhite == other.capturedWhite
				&& status == other.status && player == other.player;
		}
	};

	struct ImmediateWinStateKeyHash {
		std::size_t operator()(const ImmediateWinStateKey& key) const {
			std::size_t h = static_cast<std::size_t>(key.hash);
			h ^= static_cast<std::size_t>(key.boardSize + 17) << 1;
			h ^= static_cast<std::size_t>(key.capturedBlack + 3) << 2;
			h ^= static_cast<std::size_t>(key.capturedWhite + 7) << 3;
			h ^= static_cast<std::size_t>(key.status) << 4;
			h ^= static_cast<std::size_t>(key.player == GameState::PlayerColor::Black ? 1 : 2) << 5;
			return h;
		}
	};

	std::unordered_map<TTKey, TTEntry, TTKeyHash> tt;
	std::unordered_map<MoveCacheKey, double, MoveCacheKeyHash> moveCache;
	std::unordered_map<ImmediateWinKey, bool, ImmediateWinKeyHash> immediateWinMove;
	std::unordered_map<ImmediateWinStateKey, bool, ImmediateWinStateKeyHash> immediateWinState;
	std::unordered_map<StateKey, std::vector<StateKey>, StateKeyHash> edges;
	StateKey root;
	bool hasRoot = false;
	std::size_t ttSize = 0;
};

struct AIScoreSettings {
	int depth;
	int timeoutMs;
	int boardSize;
	GameState::PlayerColor player;
	std::function<void(const GameState&)> onGhostUpdate;
	AISearchCache* cache = nullptr;
};

class AIScoring {
public:
	static std::vector<double> scoreBoard(const GameState& state, const Rules& rules, const AIScoreSettings& settings);
	static std::size_t transpositionSize(const AISearchCache& cache);
	static void rerootCache(AISearchCache& cache, const GameState& state);
};

#endif
