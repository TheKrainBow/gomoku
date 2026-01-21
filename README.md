# Gomoku

## Overview
This is a Gomoku (Pente-like) implementation in C++ with SDL2 rendering. It supports human vs AI play, captures, forbidden double-three for Black, capture win conditions, and alignment wins with capture break checks.

## Dependencies
- SDL2 (via `sdl2-config`)

## Build
```
make
```

## Run
```
./Gomoku
```

## Controls
- Left click to place a stone (human player only)

## Game Rules Implemented
- Board size default is 19x19.
- Captures (pairs): placing a stone captures any opponent pairs in the pattern `P O O P` along any of 8 directions; captured stones are removed immediately.
- Capture win: a player wins after capturing 10 stones (5 pairs). This threshold is configurable.
- Alignment win: 5+ in a row wins unless the opponent can break the alignment by a capture on their next move.
- Forbidden double-three (Black only by default): Black cannot place a move that creates two separate open-threes at once. Open-threes include `_XXX_`, `_XX_X_`, and `_X_XX_` in any of 4 directions.
- Draw: no empty cells remain.

## Logging & Timing
- Console logs are designed to be readable and aligned, with color output.
- Each move logs as:
  - `[WHITE] played at [x,y] in T | [Y/10] (+N!)`
  - `T` is colored by AI performance (green <= 400ms, orange > 400ms, red > 500ms).
  - Human time remains white.
- The turn timer starts when a player’s turn begins and ends when their move is applied.
- Illegal moves log a reason (e.g., occupied, forbidden double three, out of bounds).
- Wins log the reason (capture or alignment).

## UI Notes
- Stones are drawn in black/white on the grid.
- The last move is marked with a small red dot.
- Winning alignment stones are circled in red.

## Project Structure
- `src/Core/`
  - Core game logic: `Board`, `Rules`, `Game`, `GameState`, `Move`, `MoveHistory`, `GameSettings`, `GameController`.
- `src/Players/`
  - Player implementations: `IPlayer`, `AIPlayer`, `HumanPlayer`.
- `src/UI/`
  - SDL2 front-end: `SdlApp`, `BoardRenderer`, `UiLayout`, `CoordinateMapper`.
- `src/Debug/`
  - Debug tests for captures and double-three detection.

## Debug Tests
Compile and run the lightweight self-check:
```
make clean && make CXXFLAGS="-std=c++20 -Wall -Wextra -Werror -DDEBUG_TESTS"
./Gomoku
```

## Configuration
- Defaults live in `src/Core/GameSettings.cpp`.
- Notable settings:
  - `boardSize`
  - `winLength`
  - `captureWinStones`
  - `forbidDoubleThreeForBlack` / `forbidDoubleThreeForWhite`
  - `aiMoveDelayMs`
