package service

import (
	"math/rand/v2"
)

// tabNames is the canonical pool of names the worker picks from when a
// caller (CLI or UI) opens an agent / terminal without an explicit
// --title. Lives only here now: previously the frontend picked names
// client-side, which left CLI-created tabs nameless. The worker is the
// single naming authority — the frontend reads `tab.title` from the
// hub's WorkspaceTab record, no local fallback.
var tabNames = []string{
	"Aaliyah", "Aaron", "Abby", "Abel", "Adam", "Addie", "Adrian", "Aiden",
	"Albie", "Alex", "Alfie", "Allie", "Alma", "Amber", "Amelia", "Anna",
	"Annie", "Aria", "Ariana", "Arlo", "Arthur", "Asher", "Athena",
	"Aubrey", "Audrey", "Aurora", "Ava", "Avery", "Bart", "Bea", "Beau",
	"Becca", "Beckett", "Bella", "Ben", "Bennett", "Birdie", "Brent",
	"Brielle", "Brody", "Brooklyn", "Brooks", "Caleb", "Cam", "Camila",
	"Carrie", "Carter", "Cassius", "Charlie", "Chloe", "Chris", "Cleo",
	"Cole", "Connor", "Cooper", "Cora", "Curtis", "Daisy", "Dan",
	"Daphne", "Dave", "Declan", "Drew", "Dylan", "Easton", "Eddie",
	"Eden", "Edie", "Effie", "Elena", "Eli", "Eliza", "Ellie", "Eloise",
	"Emily", "Emma", "Erin", "Esme", "Ethan", "Etta", "Eva", "Everett",
	"Ezra", "Felix", "Finn", "Florence", "Frankie", "Freddie", "Freya",
	"Gabe", "Genesis", "George", "Gianna", "Gigi", "Glenn", "Grace",
	"Greg", "Greta", "Gus", "Hannah", "Harper", "Harry", "Hazel", "Heath",
	"Henry", "Holly", "Hope", "Hudson", "Hugo", "Hunter", "Ian", "Idris",
	"Imogen", "Iris", "Isaac", "Isla", "Ivy", "Jack", "Jade", "Jake",
	"Jameson", "Jamie", "Jasper", "Jaxon", "Jenna", "Jenson", "Jess",
	"Joe", "Josh", "Jude", "Jules", "Kai", "Kane", "Katie", "Kayla",
	"Kennedy", "Landon", "Lara", "Layla", "Leah", "Lena", "Leo", "Levi",
	"Liam", "Lila", "Liliana", "Lily", "Lincoln", "Logan", "Lola",
	"Lottie", "Luca", "Lucy", "Luke", "Lyla", "Mabel", "Mae", "Maddie",
	"Maeve", "Margot", "Marty", "Mary", "Mason", "Mateo", "Matt",
	"Maverick", "Maya", "Megan", "Mia", "Mike", "Mila", "Miles", "Mimi",
	"Mitch", "Mona", "Naomi", "Natalie", "Nate", "Niamh", "Nick", "Nina",
	"Noah", "Nora", "Ollie", "Olivia", "Ophelia", "Otis", "Owen",
	"Patrick", "Pearl", "Penny", "Phil", "Phoebe", "Polly", "Poppy",
	"Quinn", "Ralph", "Reagan", "Reggie", "Reuben", "Rex", "Riley",
	"Rob", "Robyn", "Roman", "Ronan", "Ronnie", "Rory", "Rose", "Rowan",
	"Roxy", "Roy", "Ruby", "Ryan", "Sadie", "Sam", "Sasha", "Saskia",
	"Savannah", "Scarlett", "Seb", "Sienna", "Silas", "Skye", "Sophie",
	"Stan", "Stella", "Sullivan", "Tabby", "Tess", "Theo", "Tilda",
	"Tilly", "Tim", "Toby", "Tom", "Tony", "Trixie", "Tyler", "Vic",
	"Vince", "Violet", "Vivi", "Wade", "Walker", "Wendy", "Wes",
	"Weston", "Will", "Willow", "Wren", "Wyatt", "Xavier", "Zara",
	"Zelda", "Zoe",
}

// pickTabName returns a uniformly random name from the pool. With 256
// names, collisions are improbable for typical workspaces; we chose
// random-with-collisions over query-the-DB-to-dedup because the spawn
// hot path doesn't need a name uniqueness invariant — duplicates are
// cosmetic and the user can rename either tab.
func pickTabName() string {
	return tabNames[rand.IntN(len(tabNames))]
}

// pickAgentTitle returns "Agent <name>". See pickTabName for the
// collision policy.
func pickAgentTitle() string {
	return "Agent " + pickTabName()
}

// pickTerminalTitle returns "Terminal <name>".
func pickTerminalTitle() string {
	return "Terminal " + pickTabName()
}
