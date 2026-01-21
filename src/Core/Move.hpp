#ifndef MOVE_HPP
#define MOVE_HPP

class Move {
public:
	int x;
	int y;

	Move();
	Move(int xPos, int yPos);

	bool operator==(const Move& other) const;
	bool isValid(int boardSize) const;
};

#endif
