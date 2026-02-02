#include "GameState.hpp"

GameState::GameState()
	: toMove(PlayerColor::Black),
	  status(Status::Running),
	  hasLastMove(false),
	  capturedStonesBlack(0),
	  capturedStonesWhite(0),
	  mustCapture(false),
	  forcedCaptureMoves(),
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
	mustCapture = false;
	forcedCaptureMoves.clear();
	lastMessage.clear();
	winningLine.clear();
}
