#include "GameController.hpp"
#include "GameSettings.hpp"
#include "SdlApp.hpp"
#include "UiLayout.hpp"

#include <algorithm>
#include <cctype>
#include <iostream>
#include <string>

#ifdef DEBUG_TESTS
#include "DebugTests.hpp"
#endif

namespace {
bool parsePlayerType(const std::string& value, GameSettings::PlayerType& outType) {
	std::string lower = value;
	std::transform(lower.begin(), lower.end(), lower.begin(), [](unsigned char c) {
		return static_cast<char>(std::tolower(c));
	});
	if (lower == "ai" || lower == "ia" || lower == "bot") {
		outType = GameSettings::PlayerType::AI;
		return true;
	}
	if (lower == "human" || lower == "player") {
		outType = GameSettings::PlayerType::Human;
		return true;
	}
	return false;
}

void printUsage(const char* exe) {
	std::cout << "Usage: " << exe << " [--black ai|human] [--white ai|human]\n"
	          << "       " << exe << " [-b ai|human] [-w ai|human]\n";
}
}  // namespace

int main(int argc, char** argv) {
#ifdef DEBUG_TESTS
	runDebugTests();
	return 0;
#endif
	GameSettings settings;
	for (int i = 1; i < argc; ++i) {
		std::string arg = argv[i];
		std::string value;
		if (arg == "--help" || arg == "-h") {
			printUsage(argv[0]);
			return 0;
		}
		if ((arg == "--black" || arg == "-b") && i + 1 < argc) {
			value = argv[++i];
			if (!parsePlayerType(value, settings.blackType)) {
				std::cerr << "Invalid black player type: " << value << std::endl;
				printUsage(argv[0]);
				return 1;
			}
			continue;
		}
		if ((arg == "--white" || arg == "-w") && i + 1 < argc) {
			value = argv[++i];
			if (!parsePlayerType(value, settings.whiteType)) {
				std::cerr << "Invalid white player type: " << value << std::endl;
				printUsage(argv[0]);
				return 1;
			}
			continue;
		}
		if (arg.rfind("--black=", 0) == 0) {
			value = arg.substr(8);
			if (!parsePlayerType(value, settings.blackType)) {
				std::cerr << "Invalid black player type: " << value << std::endl;
				printUsage(argv[0]);
				return 1;
			}
			continue;
		}
		if (arg.rfind("--white=", 0) == 0) {
			value = arg.substr(8);
			if (!parsePlayerType(value, settings.whiteType)) {
				std::cerr << "Invalid white player type: " << value << std::endl;
				printUsage(argv[0]);
				return 1;
			}
			continue;
		}
		std::cerr << "Unknown argument: " << arg << std::endl;
		printUsage(argv[0]);
		return 1;
	}
	GameController controller(settings);
	UiLayout layout(settings.boardSize);
	SdlApp app(controller, layout);
	app.run();
	return 0;
}
