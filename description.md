# rename binding

the term "binding" is used throughout to represent the record
registering a resolver.

the term "binding" is far too vague for this role, as everywhere
in the docs where it is used, the reader must search to see if
"binding" refers to another binding, such as a value binding to
a variable, or a server binding to a tcp addr or the like.

it needs to be renamed into something which more clearly denotes
the resolver record that registers the resolver.   Probably
"resolver record" might work