###A fast download program

一般下载工具，都是按顺序下载，且服务器给一个每个链接提供的流量是有限制的
而fget是建立多个链接，同时下载，最后合并成目标文件。

- ####usage
```cassandraql
  -H value
        http headers
  -o string
        out put file name
  -proxy value
        proxys url eg: http://192.168.1.2:8080
  -ps int
        block size (default 10)
  -th int
        thread counts (default 10)
  -uri string
        download url
```

- ####examples
1. `fget -uri http://xxxxx`

2. `fget -uri http://xxxxx -H "Referer: http://test.com" -H "Cookies: xxxxx"`