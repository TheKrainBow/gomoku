package main

type IPlayer interface {
	IsHuman() bool
	ChooseMove(state GameState, rules Rules) Move
}
