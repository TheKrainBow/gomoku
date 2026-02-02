#ifndef GAMECONTROLLER_HPP
#define GAMECONTROLLER_HPP

#include "Game.hpp"

class GameController {
public:
	explicit GameController(const GameSettings& settings);

	void onCellClicked(int x, int y);
	void tick();
	const GameState& state() const;
	const MoveHistory& history() const;
	bool hasGhostBoard() const;
	Board ghostBoard() const;

private:
	Game game;
};

#endif
