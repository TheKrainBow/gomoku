#include "DebugTests.hpp"

#include <cassert>
#include <iostream>
#include <vector>

#include "Board.hpp"
#include "GameSettings.hpp"
#include "Rules.hpp"

void runDebugTests() {
	GameSettings settings;
	settings.boardSize = 9;
	Rules rules(settings);

	Board captureBoard(settings.boardSize);
	captureBoard.set(2, 4, Board::Cell::White);
	captureBoard.set(3, 4, Board::Cell::White);
	captureBoard.set(4, 4, Board::Cell::Black);
	Move captureMove(1, 4);
	captureBoard.set(captureMove.x, captureMove.y, Board::Cell::Black);
	std::vector<Move> captures = rules.findCaptures(captureBoard, captureMove, Board::Cell::Black);
	assert(captures.size() == 2);
	for (const Move& captured : captures) {
		captureBoard.remove(captured.x, captured.y);
	}
	assert(captureBoard.isEmpty(2, 4));
	assert(captureBoard.isEmpty(3, 4));

	Board doubleThreeBoard(settings.boardSize);
	doubleThreeBoard.set(4, 5, Board::Cell::Black);
	doubleThreeBoard.set(6, 5, Board::Cell::Black);
	doubleThreeBoard.set(5, 4, Board::Cell::Black);
	doubleThreeBoard.set(5, 6, Board::Cell::Black);
	Move doubleThreeMove(5, 5);
	bool forbidden = rules.isForbiddenDoubleThree(doubleThreeBoard, doubleThreeMove, GameState::PlayerColor::Black);
	assert(forbidden);

	std::cout << "Debug tests passed." << std::endl;
}
