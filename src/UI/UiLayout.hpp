#ifndef UILAYOUT_HPP
#define UILAYOUT_HPP

class UiLayout {
public:
	int windowWidth;
	int windowHeight;
	int padding;
	int boardPixelSize;
	int cellSize;
	int stoneRadius;
	int boardX;
	int boardY;
	int boardSize;

	UiLayout();
	explicit UiLayout(int boardSizeIn);
	void updateForWindow(int width, int height, int boardSizeIn);
};

#endif
