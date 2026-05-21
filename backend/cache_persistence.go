package main

func persistCaches() {
	persistTTPersistence(GetConfig(), SharedSearchCache())
}

func loadPersistedCaches() {
	loadTTPersistence(GetConfig(), SharedSearchCache())
}
