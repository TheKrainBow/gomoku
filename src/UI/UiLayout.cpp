#include "UiLayout.hpp"

UiLayout::UiLayout() : UiLayout(19) {
}

UiLayout::UiLayout(int boardSizeIn) {
	updateForWindow(900, 900, boardSizeIn);
}

void UiLayout::updateForWindow(int width, int height, int boardSizeIn) {
	windowWidth = width;
	windowHeight = height;
	padding = 40;
	boardSize = boardSizeIn;
	int minSize = (width < height) ? width : height;
	boardPixelSize = minSize - padding * 2;
	if (boardPixelSize < 100) {
		boardPixelSize = minSize;
	}
	cellSize = boardPixelSize / (boardSize - 1);
	boardX = (width - boardPixelSize) / 2;
	boardY = (height - boardPixelSize) / 2;
	stoneRadius = cellSize * 4 / 10;
}
