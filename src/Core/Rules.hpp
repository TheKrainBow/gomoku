#ifndef RULES_HPP
#define RULES_HPP

#include <string>
#include <vector>
#include "Board.hpp"
#include "GameState.hpp"
#include "GameSettings.hpp"
#include "Move.hpp"

class Rules {
public:
	explicit Rules(const GameSettings& settings);

	bool isLegal(const GameState& state, const Move& move, std::string* reason = nullptr) const;
	bool isLegal(const GameState& state, const Move& move, GameState::PlayerColor player, std::string* reason = nullptr) const;
	bool isWin(const Board& board, const Move& lastMove) const;
	bool isDraw(const Board& board) const;
	bool isForbiddenDoubleThree(Board& board, const Move& move, GameState::PlayerColor player) const;
	std::vector<Move> findCaptures(const Board& board, const Move& move, Board::Cell playerCell) const;
	bool opponentCanBreakAlignmentByCapture(const GameState& afterMoveState, GameState::PlayerColor opponent) const;
	std::vector<Move> findAlignmentBreakCaptures(const GameState& afterMoveState, GameState::PlayerColor opponent) const;
	bool findAlignmentLine(const Board& board, const Move& lastMove, std::vector<Move>& outLine) const;
	int getWinLength() const;
	int getCaptureWinStones() const;

private:
	GameSettings settings;

	int countDirection(const Board& board, const Move& start, int dx, int dy) const;
	void collectLine(const Board& board, const Move& start, int dx, int dy, std::vector<Move>& line) const;
	bool isOpenThreeInDirection(const Board& board, const Move& move, int dx, int dy, Board::Cell playerCell) const;
	bool hasAnyAlignment(const Board& board, Board::Cell playerCell) const;
};

#endif
