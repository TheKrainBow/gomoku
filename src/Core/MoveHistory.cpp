#include "MoveHistory.hpp"

void MoveHistory::clear() {
	entries.clear();
}

void MoveHistory::push(const HistoryEntry& entry) {
	entries.push_back(entry);
}

size_t MoveHistory::size() const {
	return entries.size();
}

const std::vector<MoveHistory::HistoryEntry>& MoveHistory::all() const {
	return entries;
}
