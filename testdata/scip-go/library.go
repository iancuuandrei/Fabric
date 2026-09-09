package scipfixture

// Greeter has one implementation in this fixture.
type Greeter interface { Greet() string }

type Person struct { Name string }

func (p Person) Greet() string { return "Salut, " + p.Name }

func Welcome(g Greeter) string { return g.Greet() }

func Example() string {
    persoană := Person{Name: "Ana"}
    return Welcome(persoană)
}
