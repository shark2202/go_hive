package main

func repeat(x string) {
	pair(x, x)
	pair(x, "other")
	pair()
}

func check(x int) {
	if x > 0 {
		println("positive")
		println("done")
	}
}
