#ifndef GAMESETTINGS_HPP
#define GAMESETTINGS_HPP

class GameSettings {
public:
	enum class PlayerType { Human, AI };

	int boardSize;
	int winLength;
	PlayerType blackType;
	PlayerType whiteType;
	bool blackStarts;
	int aiMoveDelayMs;
	int captureWinStones;
	bool forbidDoubleThreeForBlack;
	bool forbidDoubleThreeForWhite;

	GameSettings();
};

#endif
