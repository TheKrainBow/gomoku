#ifndef HUMANPLAYER_HPP
#define HUMANPLAYER_HPP

#include "IPlayer.hpp"

class HumanPlayer : public IPlayer {
public:
	HumanPlayer();
	bool isHuman() const override;
	Move chooseMove(const GameState& state, const Rules& rules) override;

	void setPendingMove(const Move& move);
	bool hasPendingMove() const;
	Move takePendingMove();

private:
	bool pending;
	Move pendingMove;
};

#endif
