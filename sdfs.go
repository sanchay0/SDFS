package main

import (
  "SDFS/protocol-buffer"
  "fmt"
  "io"
  "io/ioutil"
  "strconv"
  "os"
  "net"
  "time"
  "path/filepath"
  "github.com/golang/protobuf/proto"
  "github.com/golang/protobuf/ptypes"
  google_protobuf "github.com/golang/protobuf/ptypes/timestamp"
)
//TODO: 1) handle ctrl+C and send filemap back, OR line 353, update filemap accordingly (remove stuff).
//Shiming already does that, check line 672 in backup.go - Done
//2) whenever a node joins, look into files folder and delete everything. - Done
//3) dynamic update of replicas by looking into the membership list
//4) write prompt within 1 minute - Done
/****************************************/
/****************  SDFS  ****************/
/****************************************/
const (
	sdfsPort = ":4040"
)

var (
  primaryMaster   = 1
  secondaryMaster = 2
  thirdMaster     = 3
  fileMap         = make(map[string]*heartbeat.MapValues)
  updateMap       = make(map[string]*google_protobuf.Timestamp)
	sdfsPacket      = &heartbeat.SdfsPacket{Source: uint32(vmID)}
  localFile       string
  replica1        = vmID + 1
  replica2        = vmID + 2
)

/**
 File op: PUT, put a local file with filename @localFileName into sdfs with file name @sdfsFileName
*/
func putFile(localFileName string, sdfsFileName string) {
  // check if update is being made within 1 minute
  _, exist := updateMap[sdfsFileName]
  if exist {
    upTime := convertTime(updateMap[sdfsFileName])
    oneMinute := int(time.Minute)
    elapsed := int(time.Now().Sub(upTime))
    if elapsed <= oneMinute {
      L:
      for {
        fmt.Println("Are you sure you want to proceed with the update? (Yes/No): ")
        var input string
        fmt.Scanln(&input)
        switch input {
        case "Yes":
          break L
        case "No":
          return
        default:
          fmt.Println("Incorrect input. Please try again.")
        }
      }
    }
  }

	if vmID == primaryMaster {
		updateFileMap(sdfsFileName, uint32(vmID))
	} else {
		// not main master, send msg to master and add files into filemap
		sendSDFSMessage(primaryMaster, "add", sdfsFileName, nil)
	}

	makeLocalReplicate(sdfsFileName, localFileName)
	replicate(sdfsFileName, vmID)
}

/**
  File op: STORE, Prints the files on the current node.
*/
func store() {
  searchDir := "files/"
  fileList := getAllFiles(searchDir)
  if len(fileList) == 1 {
    fmt.Println("Nothing stored on this vm!")
    return
  }
  fmt.Printf("Files currently being stored on node %d are: \n", vmID)
  for idx, file := range fileList {
    if idx != 0 {
      fmt.Println(file)      
    }
  }
}

/**
  Get list of all files in a folder.
*/
func getAllFiles(searchDir string) []string {
  fileList := []string{}
  //iterate through a folder: https://gist.github.com/francoishill/a5aca2a7bd598ef5b563
  filepath.Walk(searchDir, func(path string, f os.FileInfo, err error) error {
    fileList = append(fileList, path)
    return nil
  })
  fmt.Println("STOREEEE :", fileList)
  return fileList
}

/**
 File op: GET
*/
func getFile(localFileName string, sdfsFileName string) {
  vals := searchFileMap(sdfsFileName)
  if vals == nil {
    fmt.Printf("Trying to GET on a file %s which doesn't exist.\n", sdfsFileName)
  } else {
    //choose first node to get the file from
    chosenIdx := vals[0]
    localFile = localFileName
    //send msg to retrieve file
    sendSDFSMessage(int(chosenIdx), "retrieve", sdfsFileName, nil)
  }
}

/**
 File op: DELETE
*/
func deleteFile(sdfsFileName string) {
  var firstPeer = vmID + 1
  var secondPeer = vmID + 2
  if secondPeer > 10 {
    secondPeer = 1
  }
  if firstPeer > 10 {
    firstPeer = 1
    secondPeer = 2
  }
  // tell replicas to delete the file
  sendSDFSMessage(firstPeer, "delete", sdfsFileName, nil)
  sendSDFSMessage(secondPeer, "delete", sdfsFileName, nil)

  //delete on self
  deleteHelper(sdfsFileName)

  // tell the primary node to update its filemap too
  sendSDFSMessage(primaryMaster, "deletePrimary", sdfsFileName, nil)
  fmt.Printf("%s successfully deleted!\n", sdfsFileName)
}

/**
  Method to simply delete file locally and remove entry from filemap.
*/
func deleteHelper(sdfsFileName string) {
  var err = os.Remove("files/" + sdfsFileName)
  if err != nil {
    fmt.Println("Error (deleting): ", err.Error())
    return
  }

  //first delete from fileMap
  _, exist := fileMap[sdfsFileName]
  if exist {
    delete(fileMap, sdfsFileName)
  }

  //now delete from updateMap
  _, exist_ := updateMap[sdfsFileName]
  if exist_ {
    delete(updateMap, sdfsFileName)
  }
}

/**
 File op: LSFILE
*/
func lsFile(sdfsFileName string) {
  fmt.Printf("%s is present on VMs with VM ids: ", sdfsFileName)
  vals := searchFileMap(sdfsFileName)
  if vals == nil {
    fmt.Printf("%s doesn't exist on SDFS.\n", sdfsFileName)
  } else {
    for _, val := range vals {
      fmt.Printf("%d, ", val)
    }
    fmt.Println()
  }
}

/**
  Search the file map with key @sdfsFileName
*/
func searchFileMap(sdfsFileName string) []uint32 {
  for k, v := range fileMap {
    if k == sdfsFileName {
      return v.GetValues()
    }
  }
  return nil
}

/**
	Utility method to update current nodes filemap.
*/
func updateFileMap(sdfsFileName string, vmID uint32) {
	var firstPeer = vmID + 1
	var secondPeer = vmID + 2
	if secondPeer > 10 {
		secondPeer = 1
	}
	if firstPeer > 10 {
		firstPeer = 1
		secondPeer = 2
	}
	//update the current nodes filemap
  var node_ids heartbeat.MapValues /* used for filemap value */
  var vals []uint32
  vals = append(vals, vmID)
  vals = append(vals, firstPeer)
  vals = append(vals, secondPeer)
  node_ids.Values = vals
  if fileMap[sdfsFileName] == nil {
    fileMap[sdfsFileName] = new(heartbeat.MapValues)
  }
	fileMap[sdfsFileName] = &node_ids

  //now update the timestamp for put file op
  if updateMap[sdfsFileName] == nil {
    updateMap[sdfsFileName] = new(google_protobuf.Timestamp)
  }
  updateMap[sdfsFileName] = ptypes.TimestampNow()
}

/**
	Make local replica of file with name @sdfsFileName, @localFileName should already exist.
	https://stackoverflow.com/questions/21060945/simple-way-to-copy-a-file-in-golang
*/
func makeLocalReplicate(sdfsFileName string, localFileName string) {
	in, err := os.Open(localFileName)
  if err != nil {
		fmt.Println("Error (while opening during local replication): ", err)
		myLog.Fatal(err)
    return
  }
  defer in.Close()
  out, err := os.Create("files/" + sdfsFileName)
  if err != nil {
		fmt.Println("Error (while creating local replication): ", err)
		myLog.Fatal(err)
    return
  }
	defer out.Close()
  if _, err = io.Copy(out, in); err != nil {
		fmt.Println("Error (while copying locally): ", err)
		myLog.Fatal(err)
    return
  }
	//flush the file to stable storage
  out.Sync()
}

/**
	Replicate the file with name sdfsFileName on nodeID + 1, nodeID + 2.
*/
func replicate(sdfsFileName string, nodeID int) {
	var firstPeer = nodeID + 1
	var secondPeer = nodeID + 2
	if secondPeer > 10 {
		secondPeer = 1
	}
	if firstPeer > 10 {
		firstPeer = 1
		secondPeer = 2
	}
	fi := readFile(sdfsFileName)
	if fi == nil {
		fmt.Println("error in reading file (while replicating).")
		return
	}
	sendSDFSMessage(firstPeer, "file", sdfsFileName, fi)
	sendSDFSMessage(secondPeer, "file", sdfsFileName, fi)
}

/**
  Read file into byte array.
*/
func readFile(sdfsFileName string) []byte {
  fi, err := ioutil.ReadFile("files/" + sdfsFileName)
  if err != nil {
    fmt.Printf("Error (while reading file into byte array): %s\n", err)
    return nil
  }
  return fi
}

/**
	Send an sdfs message packet to node with id @nodeID
*/
func sendSDFSMessage(nodeID int, message string, sdfsFileName string, file []byte) {
	if iHaveLeft {
		// Do nothing if the node has left
		time.Sleep(time.Nanosecond)
		return
	}

	// construct our msg
	sdfsPacket.Msg = message
  sdfsPacket.SdfsFileName = sdfsFileName
	if file != nil {
		sdfsPacket.File = file
	}

	//Marshal the msg
	m, err := proto.Marshal(sdfsPacket)
	if err != nil {
		fmt.Printf("Error (while marshaling): %s\n", err)
		return
	}

	conn, err := net.Dial("udp", fmt.Sprintf(nodeName, nodeID, sdfsPort))
	if err != nil {
		fmt.Printf("Error (in UDP connection): %s\n", err)
		return
	}
	//defer close and write message to tcp connection
	defer conn.Close()
	conn.Write(m)
}

/**
	Receive an sdfs message packet.
*/
func receiveSDFSMessage() {
	//set up tcp listener
	conn, err := net.ListenPacket("udp", sdfsPort)
	if err != nil {
		fmt.Printf("Error (while receiving UDP msg): %s\n", err)
		return
	}
	defer conn.Close()

  // make buffer large enough, it can contain files. Here we allow upto 300MB files.
	buf := make([]byte, 3e8)
	for {
		if iHaveLeft {
			// do not update anything if the node has left
			time.Sleep(time.Nanosecond)
			continue
		}

		// continue listenning
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			fmt.Println("Error (while reading into buffer): ", err)
			return
		}
		sdfsMsg := &heartbeat.SdfsPacket{}
		if err := proto.Unmarshal(buf[0:n], sdfsMsg); err != nil {
			fmt.Printf("Error (while unmarshaling): %s\n", err)
			return
		}
		// fmt.Println("n: ", n)
		// fmt.Println(proto.MarshalTextString(sdfsMsg))
		myLog.Printf("Message sent from node %d (IP: %s).\n", sdfsMsg.GetSource(), addr.String())
    //handle the requests
    handleRequest(sdfsMsg)
	}
}

/**
  Handle the incoming requests.
*/
func handleRequest(sdfsMsg *heartbeat.SdfsPacket) {
  switch sdfsMsg.GetMsg() {
    case "add": //add a file to fileMap
      updateFileMap(sdfsMsg.GetSdfsFileName(), sdfsMsg.GetSource())
    case "file": //save a file locally (used w replication)
      saveFile(sdfsMsg.GetSdfsFileName(), sdfsMsg.GetFile(), 1)
    case "retrieve": //retrieve a file from a replica (used w get)
      fi := readFile(sdfsMsg.GetSdfsFileName())
      if fi == nil {
        fmt.Println("Error (in GET, while reading file from replica).")
        return
      }
      sendSDFSMessage(int(sdfsMsg.GetSource()), "getFile", sdfsMsg.GetSdfsFileName(),
      fi)
    case "getFile": //save file with local file name (used w get)
      saveFile(localFile, sdfsMsg.GetFile(), 0)
    case "deletePrimary": //delete file from filemap (only applicable on primaryMaster, used w delete)
      _, exist := fileMap[sdfsMsg.GetSdfsFileName()]
      if exist {
        delete(fileMap, sdfsMsg.GetSdfsFileName())
      }
    case "delete": //delete file locally and update filemap
      deleteHelper(sdfsMsg.GetSdfsFileName())
    case "addNewNode": //used with dynamic replication, to add new replicas to filemap
      num, err := strconv.Atoi(sdfsMsg.GetSdfsFileName())
      if err != nil {
        fmt.Println("Error (while dynamic change of replica)", err)
        return
      }
      putNewReplicationToFileMap(sdfsMsg.GetSource(), uint32(num))
  }
}

/**
  Only used by the primaryMaster, puts @replica into the key who's value contains @source
*/
func putNewReplicationToFileMap(source uint32, replica uint32) {
  for _, v := range fileMap {
    vals := v.GetValues()
    for _, val := range vals {
      if val == source {
        vals = append(vals, replica)
        v.Values = vals
        break
      }
    }
  }
}

/**
	Save file @file locally with name @sdfsFileName
  @getOrPut - 1 for put, 0 for get
*/
func saveFile(sdfsFileName string, file []byte, getOrPut int) {
  // set permissions, allow r/w/e by everyone in this case
  permission := 0777
  var filePath string
  if getOrPut == 0 {
    filePath = sdfsFileName
  } else {
    filePath = "files/" + sdfsFileName
  }
  //TODO: might have to decode file base64.StdEncoding.DecodeString
  err := ioutil.WriteFile(filePath, file, os.FileMode(permission))
  if err != nil {
    fmt.Println("Error (while saving file): ", err)
    return
  }
  fmt.Println("File saved (or replicated)!")
}

/**
	Utility function to print file map of node with node id @VMid
*/
func printFileMap() {
	fmt.Println("name id")
	for k, v := range fileMap {
		for _, val := range v.GetValues() {
			fmt.Printf("%s %d\n", k, val)
		}
	}
}

/**
  Tell if current node is master or not, if not, tell the correct master.
*/
func isMaster() {
  fmt.Printf("Primary master is: %d, Secondary master is: %d, Third master is: %d\n",
    primaryMaster, secondaryMaster, thirdMaster)
  if vmID == primaryMaster {
    fmt.Println("Current node is primary master node.")
  } else {
    fmt.Println("Current node is not the primary master.")
  }
}

/**
  Re-election of the master node on failing
*/
func masterElection() {
    tempMaster := 1
    if membershipList[primaryMaster-1].GetStatus() != alive {
        tempMaster = secondaryMaster
        if membershipList[secondaryMaster-1].GetStatus() != alive {
            tempMaster = thirdMaster
        }
        primaryMaster = tempMaster
    }
}

/**
  Delete (empty) files folder whenever a node joins, and create it again.
*/
func deleteAllSdfsFiles() {
  permission := 0777
  os.RemoveAll("files/")
  os.MkdirAll("files/", os.FileMode(permission))
}

/**
  Update the fileMap, used periodically by primary master to check for crashed nodes.
*/
func updatePrimaryFileMap() {
  for idx:=0; idx<listLength; idx++ {
    if membershipList[idx].GetStatus() != alive {
      removeNodeFromFileMaps(membershipList[idx].GetId() + 1)
    }
  }
}

/**
  Remove crashed node from file map and update map.
*/
func removeNodeFromFileMaps(idx uint32) {
  for _, v := range fileMap {
    vals := v.GetValues()
    for i, num := range vals {
      if num == idx {
        vals = append(vals[:i], vals[i+1:]...)
        v.Values = vals
      }
    }
  }
}

/**
  Dynamiccally update replcation nodes.
*/
func updateReplicationNodes() {
  checkReplication(replica1, 1)
  checkReplication(replica2, 2)
}

func checkReplication(replica int, replicationNumber int) {
  if membershipList[replica-1].GetStatus() == crash || membershipList[replica-1].GetStatus() == leave {
    electReplication(replica-1, replicationNumber)
  }
}

/**
  Dynamically send files to updated replication node.
*/
func electReplication(replica int, replicationNumber int) {
  idx := replica
  for count := replica; count < 20; count++ {
    if(count > 9 ) {
      idx = count - 10
	  } else{
  		idx = count
  	}
  	if membershipList[idx].GetStatus() == alive {
  		if(replicationNumber == 1) {
		    if(idx!=vmID-1 && idx!= replica2-1){
	        replica1 = idx+1
          fileList := getAllFiles("files/")
          if len(fileList) != 1 {
            for index, file := range fileList {
              if index != 0 {
                fi := readFile(file)
                sendSDFSMessage(replica1, "file", file, fi)
              }
            }
          }
          sendSDFSMessage(primaryMaster, "addNewNode", strconv.Itoa(replica1), nil)
          break
		    }
  		} else {
  		    if(idx!=vmID-1 && idx!= replica1-1) {
		        replica2 = idx+1
            fileList := getAllFiles("files/")
            if len(fileList) != 1 {
              for index, file := range fileList {
                if index != 0 {
                  fi := readFile(file)
                  sendSDFSMessage(replica2, "file", file, fi)
                }
              }
            }
            sendSDFSMessage(primaryMaster, "addNewNode", strconv.Itoa(replica2), nil)
		        break
  		    }
  		}
  	}
	}
}
