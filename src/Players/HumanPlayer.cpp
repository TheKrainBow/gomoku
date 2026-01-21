#include "HumanPlayer.hpp"

HumanPlayer::HumanPlayer() : pending(false), pendingMove() {
}

bool HumanPlayer::isHuman() const {
	return true;
}

Move HumanPlayer::chooseMove(const GameState&, const Rules&) {
	return Move();
}

void HumanPlayer::setPendingMove(const Move& move) {
	pendingMove = move;
	pending = true;
}

bool HumanPlayer::hasPendingMove() const {
	return pending;
}

Move HumanPlayer::takePendingMove() {
	pending = false;
	return pendingMove;
}
