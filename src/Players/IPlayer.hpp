#ifndef IPLAYER_HPP
#define IPLAYER_HPP

#include "GameState.hpp"
#include "Move.hpp"
#include "Rules.hpp"

class IPlayer {
public:
	virtual ~IPlayer() = default;
	virtual bool isHuman() const = 0;
	virtual Move chooseMove(const GameState& state, const Rules& rules) = 0;
};

#endif
