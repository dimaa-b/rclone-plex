## :notebook_with_decorative_cover: &nbsp;What is it?

This fork of rclone allows you to watch media that is on your Plex watchlists and trakt.tv lists, even if the media is not stored locally on your computer. This is done by creating empty files for the media items and then streaming the content from Real-Debrid when the items are fetched in Plex. This works similar to Plex-Debrid, but allows you to create an unlimited library and creates an experience similar to Streamio. 

## Installation & documentation

Only tested on Windows
1. Download the program and compile it with the following command:
```go build -tags cmount```

2. Configure the Plex remote within rclone config
```./rclone config``` 

3. Mount the remote with the following command:
```./rclone mount (mount name): Y: --dir-cache-time 10s```

4. Add the libraries into Plex
    - The program will start filling in the files from your Plex watchlist and trakt.tv lists. This may take some time, depending on the size of your lists.

5. Once a play is detected, the program will add a link to the file, and it will start playing ONLY within Plex. The files cannot be played anywhere and will send an empty file. 

Please note that this program is in very early development and contains bugs.

## Downloads

  * https://rclone.org/downloads/

License
-------

This is free software under the terms of the MIT license (check the
[COPYING file](/COPYING) included in this package).
