#include "GameController.hpp"

GameController::GameController(const GameSettings& settings) : game(settings) {
}

void GameController::onCellClicked(int x, int y) {
	game.submitHumanMove(Move(x, y));
}

void GameController::tick() {
	game.tick();
}

const GameState& GameController::state() const {
	return game.getState();
}

const MoveHistory& GameController::history() const {
	return game.getHistory();
}
