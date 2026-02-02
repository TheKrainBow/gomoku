#ifndef GAMESTATE_HPP
#define GAMESTATE_HPP

#include <string>
#include <vector>
#include "Board.hpp"
#include "GameSettings.hpp"
#include "Move.hpp"

class GameState {
public:
	enum class PlayerColor { Black, White };
	enum class Status { Running, BlackWon, WhiteWon, Draw };

	Board board;
	PlayerColor toMove;
	Status status;
	bool hasLastMove;
	Move lastMove;
	int capturedStonesBlack;
	int capturedStonesWhite;
	bool mustCapture;
	std::vector<Move> forcedCaptureMoves;
	std::string lastMessage;
	std::vector<Move> winningLine;

	GameState();
	void reset(const GameSettings& settings);
};

#endif
