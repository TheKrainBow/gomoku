#ifndef BOARD_HPP
#define BOARD_HPP

#include <vector>

class Board {
public:
	enum class Cell { Empty, Black, White };

	Board();
	explicit Board(int boardSize);

	Cell at(int x, int y) const;
	void set(int x, int y, Cell value);
	void remove(int x, int y);
	bool inBounds(int x, int y) const;
	bool isEmpty(int x, int y) const;
	int countEmpty() const;
	void reset(int boardSize);

	int getSize() const;

private:
	int size;
	std::vector<Cell> cells;

	int index(int x, int y) const;
};

#endif
