#include "AIPlayer.hpp"

#include <chrono>
#include <thread>

AIPlayer::AIPlayer(int moveDelayMs) : delayMs(moveDelayMs) {
}

bool AIPlayer::isHuman() const {
	return false;
}

Move AIPlayer::chooseMove(const GameState& state, const Rules& rules) {
	if (delayMs > 0) {
		std::this_thread::sleep_for(std::chrono::milliseconds(delayMs));
	}
	int size = state.board.getSize();
	for (int y = 0; y < size; ++y) {
		for (int x = 0; x < size; ++x) {
			Move move(x, y);
			if (rules.isLegal(state, move)) {
				return move;
			}
		}
	}
	return Move();
}
