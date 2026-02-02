#ifndef AIPLAYER_HPP
#define AIPLAYER_HPP

#include <atomic>
#include <mutex>
#include <thread>

#include "AIScoring.hpp"
#include "Board.hpp"
#include "IPlayer.hpp"

class AIPlayer : public IPlayer {
public:
	explicit AIPlayer(int moveDelayMs);
	~AIPlayer();
	bool isHuman() const override;
	Move chooseMove(const GameState& state, const Rules& rules) override;
	void startThinking(const GameState& state, const Rules& rules);
	bool isThinking() const;
	bool hasMoveReady() const;
	Move takeMove();
	bool hasGhostBoard() const;
	Board ghostBoardCopy() const;
	void onMoveApplied(const GameState& state);
	std::size_t cacheSize() const;

private:
	int delayMs;
	mutable std::mutex ghostMutex;
	mutable std::mutex moveMutex;
	mutable std::mutex cacheMutex;
	std::thread worker;
	std::atomic<bool> thinking;
	std::atomic<bool> moveReady;
	std::atomic<bool> ghostActive;
	Move readyMove;
	Board ghostBoard;
	AISearchCache cache;
};

#endif
