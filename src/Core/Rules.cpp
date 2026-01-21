#include "Rules.hpp"

Rules::Rules(const GameSettings& settingsIn) : settings(settingsIn) {
}

bool Rules::isLegal(const GameState& state, const Move& move, std::string* reason) const {
	return isLegal(state, move, state.toMove, reason);
}

bool Rules::isLegal(const GameState& state, const Move& move, GameState::PlayerColor player, std::string* reason) const {
	if (!move.isValid(settings.boardSize)) {
		if (reason) {
			*reason = "out of bounds";
		}
		return false;
	}
	if (!state.board.isEmpty(move.x, move.y)) {
		if (reason) {
			*reason = "occupied";
		}
		return false;
	}
	bool forbidForPlayer = (player == GameState::PlayerColor::Black) ? settings.forbidDoubleThreeForBlack : settings.forbidDoubleThreeForWhite;
	if (forbidForPlayer) {
		Board temp = state.board;
		if (isForbiddenDoubleThree(temp, move, player)) {
			if (reason) {
				*reason = "forbidden double three";
			}
			return false;
		}
	}
	return true;
}

bool Rules::isWin(const Board& board, const Move& lastMove) const {
	if (!lastMove.isValid(settings.boardSize)) {
		return false;
	}
	Board::Cell target = board.at(lastMove.x, lastMove.y);
	if (target == Board::Cell::Empty) {
		return false;
	}

	const int directions[4][2] = { {1, 0}, {0, 1}, {1, 1}, {1, -1} };
	for (int i = 0; i < 4; ++i) {
		int dx = directions[i][0];
		int dy = directions[i][1];
		int count = 1;
		count += countDirection(board, lastMove, dx, dy);
		count += countDirection(board, lastMove, -dx, -dy);
		if (count >= settings.winLength) {
			return true;
		}
	}
	return false;
}

bool Rules::isDraw(const Board& board) const {
	return board.countEmpty() == 0;
}

bool Rules::isForbiddenDoubleThree(Board& board, const Move& move, GameState::PlayerColor player) const {
	Board::Cell cell = (player == GameState::PlayerColor::Black) ? Board::Cell::Black : Board::Cell::White;
	board.set(move.x, move.y, cell);
	int openThrees = 0;
	const int directions[4][2] = { {1, 0}, {0, 1}, {1, 1}, {1, -1} };
	for (int i = 0; i < 4; ++i) {
		int dx = directions[i][0];
		int dy = directions[i][1];
		if (isOpenThreeInDirection(board, move, dx, dy, cell)) {
			++openThrees;
			if (openThrees >= 2) {
				board.remove(move.x, move.y);
				return true;
			}
		}
	}
	board.remove(move.x, move.y);
	return openThrees >= 2;
}

std::vector<Move> Rules::findCaptures(const Board& board, const Move& move, Board::Cell playerCell) const {
	std::vector<Move> captures;
	Board::Cell opponentCell = (playerCell == Board::Cell::Black) ? Board::Cell::White : Board::Cell::Black;
	const int directions[8][2] = {
		{1, 0}, {-1, 0}, {0, 1}, {0, -1},
		{1, 1}, {-1, -1}, {1, -1}, {-1, 1}
	};
	for (int i = 0; i < 8; ++i) {
		int dx = directions[i][0];
		int dy = directions[i][1];
		int x1 = move.x + dx;
		int y1 = move.y + dy;
		int x2 = move.x + 2 * dx;
		int y2 = move.y + 2 * dy;
		int x3 = move.x + 3 * dx;
		int y3 = move.y + 3 * dy;
		if (!board.inBounds(x3, y3) || !board.inBounds(x2, y2) || !board.inBounds(x1, y1)) {
			continue;
		}
		if (board.at(x1, y1) == opponentCell && board.at(x2, y2) == opponentCell && board.at(x3, y3) == playerCell) {
			Move cap1(x1, y1);
			Move cap2(x2, y2);
			bool dup1 = false;
			bool dup2 = false;
			for (const Move& existing : captures) {
				if (existing == cap1) {
					dup1 = true;
				}
				if (existing == cap2) {
					dup2 = true;
				}
			}
			if (!dup1) {
				captures.push_back(cap1);
			}
			if (!dup2) {
				captures.push_back(cap2);
			}
		}
	}
	return captures;
}

bool Rules::opponentCanBreakAlignmentByCapture(const GameState& afterMoveState, GameState::PlayerColor opponent) const {
	GameState probeState = afterMoveState;
	probeState.toMove = opponent;
	Board::Cell opponentCell = (opponent == GameState::PlayerColor::Black) ? Board::Cell::Black : Board::Cell::White;
	Board::Cell targetCell = (opponent == GameState::PlayerColor::Black) ? Board::Cell::White : Board::Cell::Black;
	int size = afterMoveState.board.getSize();
	for (int y = 0; y < size; ++y) {
		for (int x = 0; x < size; ++x) {
			if (!afterMoveState.board.isEmpty(x, y)) {
				continue;
			}
			Move move(x, y);
			if (!isLegal(probeState, move, opponent)) {
				continue;
			}
			Board boardCopy = afterMoveState.board;
			boardCopy.set(x, y, opponentCell);
			std::vector<Move> captures = findCaptures(boardCopy, move, opponentCell);
			if (captures.empty()) {
				continue;
			}
			for (const Move& cap : captures) {
				boardCopy.remove(cap.x, cap.y);
			}
			if (!hasAnyAlignment(boardCopy, targetCell)) {
				return true;
			}
		}
	}
	return false;
}

bool Rules::findAlignmentLine(const Board& board, const Move& lastMove, std::vector<Move>& outLine) const {
	outLine.clear();
	if (!lastMove.isValid(settings.boardSize)) {
		return false;
	}
	Board::Cell target = board.at(lastMove.x, lastMove.y);
	if (target == Board::Cell::Empty) {
		return false;
	}
	const int directions[4][2] = { {1, 0}, {0, 1}, {1, 1}, {1, -1} };
	for (int i = 0; i < 4; ++i) {
		int dx = directions[i][0];
		int dy = directions[i][1];
		std::vector<Move> line;
		collectLine(board, lastMove, dx, dy, line);
		if (static_cast<int>(line.size()) >= settings.winLength) {
			outLine = line;
			return true;
		}
	}
	return false;
}

int Rules::countDirection(const Board& board, const Move& start, int dx, int dy) const {
	Board::Cell target = board.at(start.x, start.y);
	int x = start.x + dx;
	int y = start.y + dy;
	int count = 0;
	while (board.inBounds(x, y) && board.at(x, y) == target) {
		++count;
		x += dx;
		y += dy;
	}
	return count;
}

void Rules::collectLine(const Board& board, const Move& start, int dx, int dy, std::vector<Move>& line) const {
	line.clear();
	Board::Cell target = board.at(start.x, start.y);
	int x = start.x;
	int y = start.y;
	while (board.inBounds(x - dx, y - dy) && board.at(x - dx, y - dy) == target) {
		x -= dx;
		y -= dy;
	}
	while (board.inBounds(x, y) && board.at(x, y) == target) {
		line.push_back(Move(x, y));
		x += dx;
		y += dy;
	}
}

bool Rules::isOpenThreeInDirection(const Board& board, const Move& move, int dx, int dy, Board::Cell playerCell) const {
	const int range = 5;
	const int lineSize = range * 2 + 1;
	char line[lineSize];
	for (int i = -range; i <= range; ++i) {
		int x = move.x + i * dx;
		int y = move.y + i * dy;
		char value = 'O';
		if (board.inBounds(x, y)) {
			Board::Cell cell = board.at(x, y);
			if (cell == Board::Cell::Empty) {
				value = '_';
			} else if (cell == playerCell) {
				value = 'X';
			} else {
				value = 'O';
			}
		}
		line[i + range] = value;
	}
	const int center = range;
	for (int start = 0; start + 5 <= lineSize; ++start) {
		int end = start + 5;
		if (center < start || center >= end) {
			continue;
		}
		if (line[start] == '_' && line[start + 4] == '_' &&
			line[start + 1] == 'X' && line[start + 2] == 'X' && line[start + 3] == 'X') {
			return true;
		}
	}
	for (int start = 0; start + 6 <= lineSize; ++start) {
		int end = start + 6;
		if (center < start || center >= end) {
			continue;
		}
		if (line[start] != '_' || line[start + 5] != '_') {
			continue;
		}
		char c1 = line[start + 1];
		char c2 = line[start + 2];
		char c3 = line[start + 3];
		char c4 = line[start + 4];
		if (c1 == 'X' && c2 == 'X' && c3 == '_' && c4 == 'X') {
			return true;
		}
		if (c1 == 'X' && c2 == '_' && c3 == 'X' && c4 == 'X') {
			return true;
		}
	}
	return false;
}

bool Rules::hasAnyAlignment(const Board& board, Board::Cell playerCell) const {
	int size = board.getSize();
	const int directions[4][2] = { {1, 0}, {0, 1}, {1, 1}, {1, -1} };
	for (int y = 0; y < size; ++y) {
		for (int x = 0; x < size; ++x) {
			if (board.at(x, y) != playerCell) {
				continue;
			}
			Move move(x, y);
			for (int i = 0; i < 4; ++i) {
				int dx = directions[i][0];
				int dy = directions[i][1];
				int count = 1;
				count += countDirection(board, move, dx, dy);
				count += countDirection(board, move, -dx, -dy);
				if (count >= settings.winLength) {
					return true;
				}
			}
		}
	}
	return false;
}
