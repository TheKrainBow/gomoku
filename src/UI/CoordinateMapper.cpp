#include "CoordinateMapper.hpp"

#include <cmath>

CoordinateMapper::CoordinateMapper(const UiLayout& layoutIn) : layout(layoutIn) {
}

bool CoordinateMapper::pixelToCell(int px, int py, int& outX, int& outY) const {
	double fx = (px - layout.boardX) / static_cast<double>(layout.cellSize);
	double fy = (py - layout.boardY) / static_cast<double>(layout.cellSize);
	int x = static_cast<int>(std::round(fx));
	int y = static_cast<int>(std::round(fy));
	if (x < 0 || y < 0 || x >= layout.boardSize || y >= layout.boardSize) {
		return false;
	}
	outX = x;
	outY = y;
	return true;
}

void CoordinateMapper::cellToPixelCenter(int x, int y, int& outPx, int& outPy) const {
	outPx = layout.boardX + x * layout.cellSize;
	outPy = layout.boardY + y * layout.cellSize;
}
