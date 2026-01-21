#include "GameSettings.hpp"

GameSettings::GameSettings()
	: boardSize(19),
	  winLength(5),
	  blackType(PlayerType::Human),
	  whiteType(PlayerType::AI),
	  blackStarts(true),
	  aiMoveDelayMs(150),
	  captureWinStones(10),
	  forbidDoubleThreeForBlack(true),
	  forbidDoubleThreeForWhite(false) {
}
