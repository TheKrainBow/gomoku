#ifndef SDLAPP_HPP
#define SDLAPP_HPP

#include <SDL2/SDL.h>

#include "BoardRenderer.hpp"
#include "CoordinateMapper.hpp"
#include "GameController.hpp"
#include "UiLayout.hpp"

class SdlApp {
public:
	explicit SdlApp(GameController& controller, const UiLayout& layout);
	~SdlApp();

	bool init();
	void run();

private:
	GameController& controller;
	UiLayout layout;
	CoordinateMapper mapper;
	BoardRenderer renderer;
	SDL_Window* window;
	SDL_Renderer* sdlRenderer;
	bool running;
	bool sdlInitialized;

	void handleEvent(const SDL_Event& event);
	void render();
	void updateTitle();
	void shutdown();
};

#endif
