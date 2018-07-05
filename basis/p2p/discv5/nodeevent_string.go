
package discv5

import "strconv"

const _nodeEvent_name = "pongTimeoutpingTimeoutneighboursTimeout"

var _nodeEvent_index = [...]uint8{0, 11, 22, 39}

func (i nodeEvent) String() string {
	i -= 264
	if i >= nodeEvent(len(_nodeEvent_index)-1) {
		return "nodeEvent(" + strconv.FormatInt(int64(i+264), 10) + ")"
	}
	return _nodeEvent_name[_nodeEvent_index[i]:_nodeEvent_index[i+1]]
}
