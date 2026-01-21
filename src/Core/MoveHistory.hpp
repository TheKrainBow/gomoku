#ifndef MOVEHISTORY_HPP
#define MOVEHISTORY_HPP

#include <vector>
#include "GameState.hpp"
#include "Move.hpp"

class MoveHistory {
public:
	struct HistoryEntry {
		Move move;
		GameState::PlayerColor player;
		std::vector<Move> capturedPositions;
	};

	void clear();
	void push(const HistoryEntry& entry);
	size_t size() const;
	const std::vector<HistoryEntry>& all() const;

private:
	std::vector<HistoryEntry> entries;
};

#endif
