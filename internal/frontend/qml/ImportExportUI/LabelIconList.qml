// Copyright (c) 2020 Proton Technologies AG
//
// This file is part of ProtonMail Bridge.
//
// ProtonMail Bridge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// ProtonMail Bridge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with ProtonMail Bridge.  If not, see <https://www.gnu.org/licenses/>.

// List of icons for selected folders
import QtQuick 2.8
import QtQuick.Controls 2.2
import QtQml.Models 2.2
import ProtonUI 1.0
import ImportExportUI 1.0

Rectangle {
    id: root
    width: Style.main.fontSize * 2
    height: metrics.height
    property var colorList
    color: "transparent"

    DelegateModel {
        id: selectedLabels
        model     : colorList
        delegate  : Text {
            text  : metrics.text
            font  : metrics.font
            color : modelData
        }
    }

    TextMetrics {
        id: metrics
        text: Style.fa.tag
        font {
            pointSize: Style.main.fontSize * Style.pt
            family: Style.fontawesome.name
        }
    }

    Row {
        anchors.left : root.left
        spacing : {
            var n = Math.max(2,root.colorList.length)
            var tagWidth = Math.max(1.0,metrics.width)
            var space = Math.min(1*Style.px, (root.width - n*tagWidth)/(n-1)) // not more than 1px
            space = Math.max(space,-tagWidth) // not less than tag width
            return space
        }

        Repeater {
            model: selectedLabels
        }
    }
}
