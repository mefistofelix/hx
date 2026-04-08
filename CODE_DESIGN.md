Internal Design

```Pseudo
struct hx_src {
    url: string //supports url with variuos schemas and local paths: examples /file.zip ./file.zip file:///file.zip docker://busybox:1.36.1 https://github.com/golang/example 
    registry_base_url: string //used optionally only on docker: apt: apk: npm: pypi: nuget: rpm: winget:
    target: string
    platform: string
    download_only: bool
    force_no_tmp: bool //used only in case of zip files urls on server that does not support range requests, default to false
}



// hx_src source items iterator, yields hx_items
func hx_src.items(yield func(hx_item) bool) {
    //parse hx_src url
    src_url_info = url.parse(hx_src.url)
    //if schema is http[s] and domain is github.com and the path has only 2 segments or the third segment of the path is 'tree' or 'commit'
    //the set the url to github schema github://components from the orginal url, and then reparse src_url_info
    //also we support file:// schema for local files, and we fallback to consider it a local path if url.parse() fails

    //at this point we can use the schema to switch between the diffrent source implementations
    switch(src_url_info.schema) {
        case 'http': 
        case 'https':
          //archive download
          //prepare all things needed for the yield items loop
          //this preparation depends also on the download_only flag
          for {
              //initialize hx_item fields
              yield item
              if(done) break
          }
          break
         case 'nuget':
           //.....
           break
         case 'git':
         case 'github':
           //...
         //....
    }

}

struct hx_dst {
    src: src
    path: string
    skip_path_prefix: int
    skip_symlinks: 0
    include_exclude: string
    overwrite: bool

    tui: hx_tui
}

struct hx_item {
    src_stream: stream
    type: string // 'dir','file','link'
    src_url: string
    src_full_path: string
    src_link_path: string
    dst_full_path: string
    size_compressed: int
    size_extracted: int
    size: int
}

func hx_dst.get_done_sentinel_path() {

}

func hx_dst.set_done_sentinel(bool) {

}

func hx_dst.copy() {
    //copy src items to dest folder
    for item := range hx_dst.src.items() {
      //skip item with continue if skip_symlinks or include_exclude matches the item
      //initialize dst full path

      //copy src_stream loop to destination
      for {
          //from time to time while read/write io cycle from src to dest we call show_item to update the tui 
          hx_dst.tui.show_item(hx_item)
      }

    }
}



struct hx_tui { // textual user interface display state
    mode: string // 'plain'/'ansi'
}

func hx_tui.warn(string) {

}

func hx_tui.show_item(hx_item) {
    //show_item() can be called more than ome time on the same item
    //show and eventually update total cound download size extracted size etc in hx_tui state
}


func main() {
    src: hx_src
    dst: hx_dst
    tui: hx_tui
    // initilize src, dst, tui fields from arguments
    hx_dst.copy()
    hx_dst.set_done_sentinel()
}
```
