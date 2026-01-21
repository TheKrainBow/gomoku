#include "Board.hpp"

Board::Board() : size(0) {
}

Board::Board(int boardSize) : size(0) {
	reset(boardSize);
}

Board::Cell Board::at(int x, int y) const {
	return cells[index(x, y)];
}

void Board::set(int x, int y, Cell value) {
	cells[index(x, y)] = value;
}

void Board::remove(int x, int y) {
	cells[index(x, y)] = Cell::Empty;
}

bool Board::inBounds(int x, int y) const {
	return x >= 0 && y >= 0 && x < size && y < size;
}

bool Board::isEmpty(int x, int y) const {
	return inBounds(x, y) && at(x, y) == Cell::Empty;
}

int Board::countEmpty() const {
	int count = 0;
	for (size_t i = 0; i < cells.size(); ++i) {
		if (cells[i] == Cell::Empty) {
			++count;
		}
	}
	return count;
}

void Board::reset(int boardSize) {
	size = boardSize;
	cells.assign(static_cast<size_t>(size * size), Cell::Empty);
}

int Board::getSize() const {
	return size;
}

int Board::index(int x, int y) const {
	return y * size + x;
}
