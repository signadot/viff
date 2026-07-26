Currently triggers are all independent and can spawn things in parallel

the addition of tony match makes joining parall runs easy (sketch)

name: a
action:
  onDone: set done-a

name: b:
action:
  onDone: set done-b

name: join
condition:
  when: !and [ done-a done-b ]

the process graph for a subject then is visible for parallelism

but there's no visibility for sequencing

this issue suggests making

name: a
actions:
- spawn:
- spawn:

automatically add visibility to the sequence, automatically set a.done = 0 and then 1 and then 2 etc


wdyt?

can we make the tutorial work with a simple 2-sequence with a fanout and spell out the
pattern explicitly?


