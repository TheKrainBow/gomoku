#ifndef AIPLAYER_HPP
#define AIPLAYER_HPP

#include "IPlayer.hpp"

class AIPlayer : public IPlayer {
public:
	explicit AIPlayer(int moveDelayMs);
	bool isHuman() const override;
	Move chooseMove(const GameState& state, const Rules& rules) override;

private:
	int delayMs;
};

#endif
