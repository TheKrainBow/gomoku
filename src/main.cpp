#include "GameController.hpp"
#include "GameSettings.hpp"
#include "SdlApp.hpp"
#include "UiLayout.hpp"

#ifdef DEBUG_TESTS
#include "DebugTests.hpp"
#endif

int main() {
#ifdef DEBUG_TESTS
	runDebugTests();
	return 0;
#endif
	GameSettings settings;
	GameController controller(settings);
	UiLayout layout(settings.boardSize);
	SdlApp app(controller, layout);
	app.run();
	return 0;
}
