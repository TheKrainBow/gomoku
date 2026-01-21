#include "Move.hpp"

Move::Move() : x(-1), y(-1) {
}

Move::Move(int xPos, int yPos) : x(xPos), y(yPos) {
}

bool Move::operator==(const Move& other) const {
	return x == other.x && y == other.y;
}

bool Move::isValid(int boardSize) const {
	return x >= 0 && y >= 0 && x < boardSize && y < boardSize;
}
