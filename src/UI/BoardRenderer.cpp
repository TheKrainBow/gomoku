#include "BoardRenderer.hpp"

#include "CoordinateMapper.hpp"

void BoardRenderer::render(SDL_Renderer* renderer, const GameState& state, const UiLayout& layout) {
	drawGrid(renderer, layout);
	drawStones(renderer, state, layout);
}

void BoardRenderer::drawGrid(SDL_Renderer* renderer, const UiLayout& layout) {
	SDL_SetRenderDrawColor(renderer, 40, 24, 12, 255);
	int startX = layout.boardX;
	int startY = layout.boardY;
	int endX = layout.boardX + layout.boardPixelSize;
	int endY = layout.boardY + layout.boardPixelSize;
	for (int i = 0; i < layout.boardSize; ++i) {
		int x = startX + i * layout.cellSize;
		int y = startY + i * layout.cellSize;
		SDL_RenderDrawLine(renderer, x, startY, x, endY);
		SDL_RenderDrawLine(renderer, startX, y, endX, y);
	}
}

void BoardRenderer::drawStones(SDL_Renderer* renderer, const GameState& state, const UiLayout& layout) {
	CoordinateMapper mapper(layout);
	for (int y = 0; y < state.board.getSize(); ++y) {
		for (int x = 0; x < state.board.getSize(); ++x) {
			Board::Cell cell = state.board.at(x, y);
			if (cell == Board::Cell::Empty) {
				continue;
			}
			int px = 0;
			int py = 0;
			mapper.cellToPixelCenter(x, y, px, py);
			SDL_Color color = (cell == Board::Cell::Black) ? SDL_Color{20, 20, 20, 255} : SDL_Color{240, 240, 240, 255};
			drawFilledCircle(renderer, px, py, layout.stoneRadius, color);
		}
	}

	if (state.hasLastMove) {
		int px = 0;
		int py = 0;
		mapper.cellToPixelCenter(state.lastMove.x, state.lastMove.y, px, py);
		SDL_Color highlight = {220, 30, 30, 255};
		drawFilledCircle(renderer, px, py, layout.stoneRadius / 3, highlight);
	}

	if ((state.status == GameState::Status::BlackWon || state.status == GameState::Status::WhiteWon) && !state.winningLine.empty()) {
		SDL_Color winColor = {220, 30, 30, 255};
		for (const Move& move : state.winningLine) {
			int px = 0;
			int py = 0;
			mapper.cellToPixelCenter(move.x, move.y, px, py);
			drawCircleOutline(renderer, px, py, layout.stoneRadius, winColor);
		}
	}
}

void BoardRenderer::drawFilledCircle(SDL_Renderer* renderer, int cx, int cy, int radius, SDL_Color color) {
	SDL_SetRenderDrawColor(renderer, color.r, color.g, color.b, color.a);
	for (int dy = -radius; dy <= radius; ++dy) {
		for (int dx = -radius; dx <= radius; ++dx) {
			if (dx * dx + dy * dy <= radius * radius) {
				SDL_RenderDrawPoint(renderer, cx + dx, cy + dy);
			}
		}
	}
}

void BoardRenderer::drawCircleOutline(SDL_Renderer* renderer, int cx, int cy, int radius, SDL_Color color) {
	SDL_SetRenderDrawColor(renderer, color.r, color.g, color.b, color.a);
	int x = radius;
	int y = 0;
	int err = 0;
	while (x >= y) {
		SDL_RenderDrawPoint(renderer, cx + x, cy + y);
		SDL_RenderDrawPoint(renderer, cx + y, cy + x);
		SDL_RenderDrawPoint(renderer, cx - y, cy + x);
		SDL_RenderDrawPoint(renderer, cx - x, cy + y);
		SDL_RenderDrawPoint(renderer, cx - x, cy - y);
		SDL_RenderDrawPoint(renderer, cx - y, cy - x);
		SDL_RenderDrawPoint(renderer, cx + y, cy - x);
		SDL_RenderDrawPoint(renderer, cx + x, cy - y);
		++y;
		err += 1 + 2 * y;
		if (2 * (err - x) + 1 > 0) {
			--x;
			err += 1 - 2 * x;
		}
	}
}
