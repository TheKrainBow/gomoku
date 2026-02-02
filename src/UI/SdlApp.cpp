#include "SdlApp.hpp"

#include <string>

SdlApp::SdlApp(GameController& controllerIn, const UiLayout& layoutIn)
	: controller(controllerIn),
	  layout(layoutIn),
	  mapper(layoutIn),
	  renderer(),
	  window(nullptr),
	  sdlRenderer(nullptr),
	  running(false),
	  sdlInitialized(false) {
}

SdlApp::~SdlApp() {
	shutdown();
}

void SdlApp::shutdown() {
	if (sdlRenderer) {
		SDL_DestroyRenderer(sdlRenderer);
		sdlRenderer = nullptr;
	}
	if (window) {
		SDL_DestroyWindow(window);
		window = nullptr;
	}
	if (sdlInitialized) {
		SDL_Quit();
		sdlInitialized = false;
	}
}

bool SdlApp::init() {
	if (SDL_Init(SDL_INIT_VIDEO) != 0) {
		return false;
	}
	sdlInitialized = true;
	window = SDL_CreateWindow("Gomoku", SDL_WINDOWPOS_CENTERED, SDL_WINDOWPOS_CENTERED,
					 layout.windowWidth, layout.windowHeight, SDL_WINDOW_SHOWN);
	if (!window) {
		shutdown();
		return false;
	}
	sdlRenderer = SDL_CreateRenderer(window, -1, SDL_RENDERER_ACCELERATED);
	if (!sdlRenderer) {
		shutdown();
		return false;
	}
	return true;
}

void SdlApp::run() {
	if (!init()) {
		return;
	}
	running = true;
	while (running) {
		SDL_Event event;
		while (SDL_PollEvent(&event)) {
			handleEvent(event);
		}
		controller.tick();
		updateTitle();
		render();
		SDL_Delay(16);
	}
	shutdown();
}

void SdlApp::handleEvent(const SDL_Event& event) {
	if (event.type == SDL_QUIT) {
		running = false;
		return;
	}
	if (event.type == SDL_MOUSEBUTTONDOWN && event.button.button == SDL_BUTTON_LEFT) {
		int x = 0;
		int y = 0;
		if (mapper.pixelToCell(event.button.x, event.button.y, x, y)) {
			controller.onCellClicked(x, y);
		}
	}
}

void SdlApp::render() {
	SDL_SetRenderDrawColor(sdlRenderer, 210, 180, 140, 255);
	SDL_RenderClear(sdlRenderer);
	const Board* ghostBoard = nullptr;
	Board ghostCopy;
	if (controller.hasGhostBoard()) {
		ghostCopy = controller.ghostBoard();
		ghostBoard = &ghostCopy;
	}
	renderer.render(sdlRenderer, controller.state(), layout, ghostBoard);
	SDL_RenderPresent(sdlRenderer);
}

void SdlApp::updateTitle() {
	const GameState& state = controller.state();
	std::string title = "Gomoku";
	if (state.status == GameState::Status::BlackWon) {
		title += " - Black wins";
	} else if (state.status == GameState::Status::WhiteWon) {
		title += " - White wins";
	} else if (state.status == GameState::Status::Draw) {
		title += " - Draw";
	} else if (!state.lastMessage.empty()) {
		title += " - " + state.lastMessage;
	}
	SDL_SetWindowTitle(window, title.c_str());
}
