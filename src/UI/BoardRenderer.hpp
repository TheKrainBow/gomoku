#ifndef BOARDRENDERER_HPP
#define BOARDRENDERER_HPP

#include <SDL2/SDL.h>

#include "GameState.hpp"
#include "UiLayout.hpp"

class BoardRenderer {
public:
	void render(SDL_Renderer* renderer, const GameState& state, const UiLayout& layout);

private:
	void drawGrid(SDL_Renderer* renderer, const UiLayout& layout);
	void drawStones(SDL_Renderer* renderer, const GameState& state, const UiLayout& layout);
	void drawFilledCircle(SDL_Renderer* renderer, int cx, int cy, int radius, SDL_Color color);
	void drawCircleOutline(SDL_Renderer* renderer, int cx, int cy, int radius, SDL_Color color);
};

#endif
