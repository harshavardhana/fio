# Disk latency benchmarking tool

Here is an example of how to start this tool by taking `DRIVES` to benchmark, at specified `CONCURRENT` level divided across drives, with a total of `NFILES` created across drives as well. In the following example we have used `12` drives with `1200` concurrent requests across drives by creating a total of 4million objects per drive. Optionally you can also specify the file size you are interested in using `FILESIZE` env.

```
ulimit -n 65535
export DRIVES=/mnt/drive{1...12}/fio-test
export CONCURRENT=96
export FILESIZE=12KiB
export NFILES=48M
./fio++
```


The output of this command shows the average latency per write operation as a whole i.e across drives for the entire transaction, if there are offending writes at the threshold of 1sec and beyond those objects are mentioned as unexpected.

```
Mean time taken 6.423068ms
Standard deviation time taken 9.283965ms
Fastest time taken 213.433µs
Slowest time taken 1.085103551s
```

With `DEBUG=on` you can see some collection of objects which slowed down in addition to latency values

```
object 312th took more than a 1/4th of a second to write
object 483rd took more than a 1/4th of a second to write
object 582nd took more than a 1/4th of a second to write
object 255th took more than a 1/4th of a second to write
object 737th took more than a 1/4th of a second to write
object 769th took more than a 1/4th of a second to write
object 320th took more than a 1/4th of a second to write
object 760th took more than a 1/4th of a second to write
object 994th took more than a 1/4th of a second to write
object 932nd took more than a 1/4th of a second to write
object 327th took more than a 1/4th of a second to write
object 765th took more than a 1/4th of a second to write
object 386th took more than a 1/4th of a second to write
object 796th took more than a 1/4th of a second to write
object 369th took more than a 1/4th of a second to write
```


NOTE: this tool does not arbitrarily slow itself down upon latency changes, it keeps going at full throttle until all writes are complete.
