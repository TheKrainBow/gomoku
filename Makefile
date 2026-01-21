NAME := Gomoku
CXX  := c++
CXXFLAGS := -std=c++20 -Wall -Wextra -Werror -g -fsanitize=address

SRC_DIR := src
OBJ_DIR := obj

SRCS := \
	$(SRC_DIR)/main.cpp \
	$(SRC_DIR)/Core/GameSettings.cpp \
	$(SRC_DIR)/Core/Move.cpp \
	$(SRC_DIR)/Core/Rules.cpp \
	$(SRC_DIR)/Core/Board.cpp \
	$(SRC_DIR)/Core/GameState.cpp \
	$(SRC_DIR)/Core/MoveHistory.cpp \
	$(SRC_DIR)/Players/IPlayer.cpp \
	$(SRC_DIR)/Players/HumanPlayer.cpp \
	$(SRC_DIR)/Players/AIPlayer.cpp \
	$(SRC_DIR)/Debug/DebugTests.cpp \
	$(SRC_DIR)/Core/Game.cpp \
	$(SRC_DIR)/UI/SdlApp.cpp \
	$(SRC_DIR)/UI/BoardRenderer.cpp \
	$(SRC_DIR)/UI/UiLayout.cpp \
	$(SRC_DIR)/UI/CoordinateMapper.cpp \
	$(SRC_DIR)/Core/GameController.cpp

OBJS := $(SRCS:$(SRC_DIR)/%.cpp=$(OBJ_DIR)/%.o)

SDL_CFLAGS := $(shell sdl2-config --cflags 2>/dev/null)
SDL_LIBS   := $(shell sdl2-config --libs 2>/dev/null)

CPPFLAGS := $(SDL_CFLAGS) -I$(SRC_DIR) -I$(SRC_DIR)/Core -I$(SRC_DIR)/Players -I$(SRC_DIR)/UI -I$(SRC_DIR)/Debug
LDFLAGS  := $(SDL_LIBS)

all: $(NAME)

$(NAME): $(OBJS)
	$(CXX) $(CXXFLAGS) $(OBJS) -o $(NAME) $(LDFLAGS)

$(OBJ_DIR)/%.o: $(SRC_DIR)/%.cpp
	@mkdir -p $(dir $@)
	$(CXX) $(CXXFLAGS) $(CPPFLAGS) -c $< -o $@

clean:
	rm -rf $(OBJ_DIR)

fclean: clean
	rm -f $(NAME)

re: fclean all

.PHONY: all clean fclean re
