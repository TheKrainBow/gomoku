#include "GameState.hpp"

GameState::GameState()
	: toMove(PlayerColor::Black),
	  status(Status::Running),
	  hasLastMove(false),
	  capturedStonesBlack(0),
	  capturedStonesWhite(0),
	  lastMessage(),
	  winningLine() {
}

void GameState::reset(const GameSettings& settings) {
	board.reset(settings.boardSize);
	toMove = settings.blackStarts ? PlayerColor::Black : PlayerColor::White;
	status = Status::Running;
	hasLastMove = false;
	lastMove = Move();
	capturedStonesBlack = 0;
	capturedStonesWhite = 0;
	lastMessage.clear();
	winningLine.clear();
}
