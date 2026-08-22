# matching paths in the cli

the cli supports accessing the tree as a vector of steps or as a kpath (-at)

we should make the vector of steps support tony matches on each step.  eg

```
verse watch '!or [github git]'
```

should match the first step as github or git